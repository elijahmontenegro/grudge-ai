import { useCallback, useEffect, useRef, useState } from 'react'
import { useApolloClient, useMutation } from '@apollo/client/react'
import { GET_THREAD_MESSAGES, SEND_MESSAGE } from '@/graphql/operations'
import type {
  SendMessageMutation,
  SendMessageMutationVariables,
} from '@/graphql/generated/types'
import { SelectionScope } from '@/graphql/generated/types'
import type { ComposerScope } from '@/features/composer/Composer'
import type { AttachmentMeta } from './useAttachments'

interface UseSendMessage {
  send: (text: string, attachments?: AttachmentMeta[]) => Promise<void>
  /** True from the moment send() is called until the mutation
   *  resolves. The mutation's promise only completes when the
   *  runner's SendMessage returns — after every LLM round and
   *  every tool call in the turn. That's the correct moment to
   *  stop treating the UI as "streaming". */
  streaming: boolean
  error: string | null
}

/**
 * Fires SEND_MESSAGE for `threadId`, tracks an in-flight flag for
 * the duration of the mutation, and refetches the thread corpus on
 * resolve.
 */
export function useSendMessage(
  threadId: string,
  scope: ComposerScope,
): UseSendMessage {
  const [streaming, setStreaming] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const client = useApolloClient()
  const [sendMutation] = useMutation<SendMessageMutation, SendMessageMutationVariables>(
    SEND_MESSAGE,
  )
  const streamingRef = useRef(false)

  const send = useCallback(
    async (text: string, attachments?: AttachmentMeta[]) => {
      // Allow pure-attachment sends (no text) — the agent sees the
      // attachment blocks and can open them via FileRead.
      const hasAttach = !!attachments && attachments.length > 0
      if (!threadId || streamingRef.current) return
      if (!text.trim() && !hasAttach) return
      streamingRef.current = true
      setStreaming(true)
      setError(null)
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
        setError(e instanceof Error ? e.message : String(e))
      } finally {
        // Flip `streaming` off here, not in the subscription's
        // `done` handler. The mutation's promise resolves only
        // when the runner's SendMessage returns — after every LLM
        // round and every tool call in the turn. Flipping early
        // would close the subscription mid-turn and lose the
        // second round's deltas.
        streamingRef.current = false
        setStreaming(false)
      }
    },
    [client, scope, sendMutation, threadId],
  )

  // Reset the in-flight flag if the thread changes under us.
  useEffect(() => {
    streamingRef.current = false
    setStreaming(false)
    setError(null)
  }, [threadId])

  return { send, streaming, error }
}
