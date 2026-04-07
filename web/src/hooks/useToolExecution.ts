import { gql } from '@apollo/client'
import { useSubscription } from '@apollo/client/react'

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

interface ToolExecution {
  threadId: string
  callId: string
  toolName: string
  arguments: string
  status: string
  result: string | null
  isError: boolean | null
}

export function useToolExecution(threadId: string) {
  const { data, loading, error } = useSubscription<{
    toolExecution: ToolExecution
  }>(TOOL_EXECUTION_SUBSCRIPTION, {
    variables: { threadId },
    skip: !threadId,
  })

  return {
    execution: data?.toolExecution ?? null,
    loading,
    error,
  }
}
