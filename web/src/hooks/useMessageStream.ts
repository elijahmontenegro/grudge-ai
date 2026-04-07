import { gql } from '@apollo/client'
import { useSubscription } from '@apollo/client/react'

const MESSAGE_STREAM_SUBSCRIPTION = gql`
  subscription MessageStream($threadId: ID!) {
    messageStream(threadId: $threadId) {
      messageId
      delta
      thinking
      toolCall {
        id
        name
        arguments
      }
      done
      error
    }
  }
`

interface StreamEvent {
  messageId: string
  delta: string | null
  thinking: string | null
  toolCall: { id: string; name: string; arguments: string | null } | null
  done: boolean
  error: string | null
}

export function useMessageStream(threadId: string) {
  const { data, loading, error } = useSubscription<{
    messageStream: StreamEvent
  }>(MESSAGE_STREAM_SUBSCRIPTION, {
    variables: { threadId },
    skip: !threadId,
  })

  return {
    event: data?.messageStream ?? null,
    loading,
    error,
  }
}
