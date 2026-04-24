import { useCallback, useEffect, useRef, useState } from 'react'
import { useApolloClient, useMutation, useSubscription } from '@apollo/client/react'
import { GET_THREAD_MESSAGES, MESSAGE_STREAM, SEND_MESSAGE } from '@/graphql/operations'
import type {
  MessageStreamSubscription,
  MessageStreamSubscriptionVariables,
  SendMessageMutation,
  SendMessageMutationVariables,
} from '@/graphql/generated/types'
import { SelectionScope } from '@/graphql/generated/types'
import type { ComposerScope } from '@/components/organisms/Composer'
import type { AttachmentMeta } from './useAttachments'

/** Ordered timeline of what the agent emitted this turn. The agent
 *  interleaves thinking → tool_use → [tool runs] → thinking → tool_use
 *  → … within a single round, and the UI has to render them in that
 *  exact order. Collapsing all thinking into one bucket at the top
 *  destroys the story of the round. */
export type StreamItem =
  | { kind: 'thinking'; text: string }
  | { kind: 'text'; text: string }
  | { kind: 'toolCall'; id: string; name: string; arguments: string }

export interface StreamState {
  /** Interleaved sequence in arrival order. The only thing StreamingTurn
   *  should render from; `text` / `thinking` / `toolCalls` below are
   *  derived views kept for dependents that need flat accumulators. */
  items: StreamItem[]
  /** id of the in-flight assistant message (from the stream) */
  messageId: string | null
  /** Flat accumulators — derived from `items`, kept so effects that
   *  watch lengths (auto-scroll, stage-label heuristics) don't have to
   *  care about the timeline. */
  text: string
  thinking: string
  toolCalls: { id: string; name: string; arguments: string }[]
  error: string | null
}

const EMPTY_STREAM: StreamState = {
  items: [],
  text: '',
  thinking: '',
  messageId: null,
  toolCalls: [],
  error: null,
}

export interface UseSendAndStream {
  send: (text: string, attachments?: AttachmentMeta[]) => Promise<void>
  streaming: boolean
  stream: StreamState
}

interface SendAndStreamOptions {
  /** External running signal — when true, the subscription stays open even
   *  if the user didn't initiate the send from this tab (autonomous run,
   *  another client, etc). */
  externalStreaming?: boolean
}

export function useSendAndStream(
  threadId: string,
  scope: ComposerScope,
  opts: SendAndStreamOptions = {},
): UseSendAndStream {
  const [localStreaming, setLocalStreaming] = useState(false)
  const [stream, setStream] = useState<StreamState>(EMPTY_STREAM)
  const client = useApolloClient()
  const [sendMutation] = useMutation<SendMessageMutation, SendMessageMutationVariables>(
    SEND_MESSAGE,
  )
  const streamingRef = useRef(false)

  const streaming = localStreaming || !!opts.externalStreaming

  useSubscription<MessageStreamSubscription, MessageStreamSubscriptionVariables>(MESSAGE_STREAM, {
    variables: { threadId },
    skip: !threadId || !streaming,
    onData: ({ data }) => {
      const ev = data.data?.messageStream
      if (!ev) return
      setStream((s) => {
        if (ev.error) return { ...s, error: ev.error }
        const items = s.items.slice()
        let { text, thinking, toolCalls } = s

        // Thinking delta: extend the tail if it's still a thinking
        // segment, otherwise start a new thinking item. This is what
        // produces the interleaved [think][tool][think][tool] order —
        // the arrival of a toolCall event below "closes" the current
        // thinking segment, so the next thinking delta opens a fresh
        // one rather than appending into the one above the tool.
        if (ev.thinking) {
          const last = items[items.length - 1]
          if (last && last.kind === 'thinking') {
            items[items.length - 1] = { ...last, text: last.text + ev.thinking }
          } else {
            items.push({ kind: 'thinking', text: ev.thinking })
          }
          thinking = thinking + ev.thinking
        }

        // Text delta: same append-or-extend as thinking.
        if (ev.delta) {
          const last = items[items.length - 1]
          if (last && last.kind === 'text') {
            items[items.length - 1] = { ...last, text: last.text + ev.delta }
          } else {
            items.push({ kind: 'text', text: ev.delta })
          }
          text = text + ev.delta
        }

        // Tool call: append new, or accumulate arguments onto an
        // existing one. A tool_use block arriving in the middle of
        // thinking deltas closes the thinking segment (by pushing a
        // toolCall item after it), so subsequent thinking deltas
        // start a fresh thinking segment below the tool — exactly the
        // interleaving the model emitted.
        if (ev.toolCall) {
          const id = ev.toolCall.id
          const existingIdx = items.findIndex(
            (it) => it.kind === 'toolCall' && it.id === id,
          )
          if (existingIdx >= 0) {
            const existing = items[existingIdx] as Extract<StreamItem, { kind: 'toolCall' }>
            items[existingIdx] = {
              ...existing,
              arguments: existing.arguments + (ev.toolCall.arguments || ''),
            }
          } else {
            items.push({
              kind: 'toolCall',
              id,
              name: ev.toolCall.name,
              arguments: ev.toolCall.arguments ?? '',
            })
          }
          const tcExisting = toolCalls.find((c) => c.id === id)
          if (tcExisting) {
            toolCalls = toolCalls.map((c) =>
              c.id === id ? { ...c, arguments: (c.arguments || '') + (ev.toolCall!.arguments || '') } : c,
            )
          } else {
            toolCalls = [
              ...toolCalls,
              { id, name: ev.toolCall.name, arguments: ev.toolCall.arguments ?? '' },
            ]
          }
        }

        return { ...s, items, text, thinking, toolCalls, messageId: ev.messageId }
      })
      if (ev.done) {
        // `done` marks the END of one LLM round, not the end of the
        // turn. A turn with tool calls emits one `done` per round
        // (after the tool call) plus a final `done` (after the tool
        // result). Previously we flipped `localStreaming = false`
        // here, which closed the subscription between rounds — the
        // second round's deltas arrived into a dead channel and the
        // final response appeared all at once via refetch. Now we
        // only refetch (so the intermediate tool-call turn renders
        // in its settled form) and clear the stream buffer. The
        // `localStreaming` flag flips off when the `sendMutation`
        // promise resolves — that's the real end of the turn.
        void client.refetchQueries({ include: [GET_THREAD_MESSAGES] })
        setTimeout(() => setStream(EMPTY_STREAM), 80)
      }
    },
    onError: (err) => {
      streamingRef.current = false
      setLocalStreaming(false)
      setStream((s) => ({ ...s, error: err.message }))
    },
  })

  const send = useCallback(
    async (text: string, attachments?: AttachmentMeta[]) => {
      // Allow pure-attachment sends (no text) — the agent sees the
      // attachment blocks and can open them via FileRead.
      const hasAttach = !!attachments && attachments.length > 0
      if (!threadId || streamingRef.current) return
      if (!text.trim() && !hasAttach) return
      streamingRef.current = true
      setStream(EMPTY_STREAM)
      setLocalStreaming(true)
      try {
        await sendMutation({
          variables: {
            threadId,
            content: text,
            scope: scope === 'all' ? SelectionScope.AllThreads : SelectionScope.Thread,
            attachments,
          },
        })
        void client.refetchQueries({ include: [GET_THREAD_MESSAGES] })
      } catch (e) {
        setStream({
          ...EMPTY_STREAM,
          error: e instanceof Error ? e.message : String(e),
        })
      } finally {
        // Flip `localStreaming` off here, not in the subscription's
        // `done` handler. The mutation's promise resolves only when
        // the runner's SendMessage returns — after every LLM round
        // and every tool call in the turn. That's the correct moment
        // to stop treating the UI as "streaming". Flipping early
        // would close the subscription mid-turn and lose the second
        // round's deltas.
        streamingRef.current = false
        setLocalStreaming(false)
      }
    },
    [client, scope, sendMutation, threadId],
  )

  // Clear stream state if thread changes under us.
  useEffect(() => {
    setStream(EMPTY_STREAM)
    setLocalStreaming(false)
    streamingRef.current = false
  }, [threadId])

  return { send, streaming, stream }
}
