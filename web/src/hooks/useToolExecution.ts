import { gql } from '@apollo/client'
import { useSubscription } from '@apollo/client/react'
import type {
  ToolExecutionSubscription,
  ToolExecutionSubscriptionVariables,
} from '@/graphql/generated/types'

const TOOL_EXECUTION_SUBSCRIPTION = gql`
  subscription ToolExecution($threadId: ID!) {
    toolExecution(threadId: $threadId) {
      threadId
      callId
      toolName
      arguments
      status
      result
      isError
    }
  }
`

export function useToolExecution(threadId: string) {
  const { data, loading, error } = useSubscription<
    ToolExecutionSubscription,
    ToolExecutionSubscriptionVariables
  >(TOOL_EXECUTION_SUBSCRIPTION, {
    variables: { threadId },
    skip: !threadId,
  })

  return {
    execution: data?.toolExecution ?? null,
    loading,
    error,
  }
}
