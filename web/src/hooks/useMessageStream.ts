import { gql } from '@apollo/client'
import { useSubscription } from '@apollo/client/react'
import type {
  MessageStreamSubscription,
  MessageStreamSubscriptionVariables,
} from '@/graphql/generated/types'

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

export function useMessageStream(threadId: string) {
  const { data, loading, error } = useSubscription<
    MessageStreamSubscription,
    MessageStreamSubscriptionVariables
  >(MESSAGE_STREAM_SUBSCRIPTION, {
    variables: { threadId },
    skip: !threadId,
  })

  return {
    event: data?.messageStream ?? null,
    loading,
    error,
  }
}
