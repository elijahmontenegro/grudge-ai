import { useEffect, useState } from 'react'
import { useApolloClient, useSubscription } from '@apollo/client/react'
import { GET_THREAD_MESSAGES, MESSAGE_STREAM } from '@/graphql/operations'
import type {
  MessageStreamSubscription,
  MessageStreamSubscriptionVariables,
} from '@/graphql/generated/types'
import { applyStreamEvent, EMPTY_STREAM, type StreamState } from './streamReducer'

export type { StreamItem, StreamState } from './streamReducer'

interface UseMessageStream {
  stream: StreamState
}

/**
 * Subscribes to MESSAGE_STREAM for `threadId` while `active` is true
 * and folds each event into a StreamState via the pure reducer in
 * streamReducer.ts.
 *
 * On `done` events: refetch the thread corpus (so the intermediate
 * tool-call turn renders in its settled form) and clear the stream
 * buffer after a tick. The `active` flag is what decides when the
 * subscription opens / closes — the caller controls it via the
 * mutation hook (useSendMessage) or external running signals.
 */
export function useMessageStream(threadId: string, active: boolean): UseMessageStream {
  const [stream, setStream] = useState<StreamState>(EMPTY_STREAM)
  const client = useApolloClient()

  useSubscription<MessageStreamSubscription, MessageStreamSubscriptionVariables>(
    MESSAGE_STREAM,
    {
      variables: { threadId },
      skip: !threadId || !active,
      onData: ({ data }) => {
        const ev = data.data?.messageStream
        if (!ev) return
        setStream((s) => applyStreamEvent(s, ev))
        if (ev.done) {
          // `done` marks the END of one LLM round, not the end of
          // the turn. A turn with tool calls emits one `done` per
          // round (after the tool call) plus a final `done` (after
          // the tool result). The `active` flag is the caller's
          // responsibility — it should track the mutation's
          // promise, which only resolves at true turn-end. Refetch
          // so the intermediate tool-call turn renders in its
          // settled form, then clear the buffer.
          void client.refetchQueries({ include: [GET_THREAD_MESSAGES] })
          setTimeout(() => setStream(EMPTY_STREAM), 80)
        }
      },
      onError: (err) => {
        setStream((s) => ({ ...s, error: err.message }))
      },
    },
  )

  // Clear stream state if the thread changes under us.
  useEffect(() => {
    setStream(EMPTY_STREAM)
  }, [threadId])

  return { stream }
}

/** Helper: the subscription hook owns the buffer reset, but if a
 *  consumer needs to reset (e.g. on send), expose the empty state
 *  shape. */
export { EMPTY_STREAM }
