import { gql } from '@apollo/client'
import { useSubscription } from '@apollo/client/react'
import type {
  AgentStateSubscription,
  AgentStateSubscriptionVariables,
} from '@/graphql/generated/types'

const AGENT_STATE_SUBSCRIPTION = gql`
  subscription AgentState($threadId: ID!) {
    agentState(threadId: $threadId) {
      threadId
      status
      mode
      roundCount
      startedAt
      durationLimit
      elapsedTime
    }
  }
`

export function useAgentState(threadId: string) {
  const { data, loading, error } = useSubscription<
    AgentStateSubscription,
    AgentStateSubscriptionVariables
  >(AGENT_STATE_SUBSCRIPTION, {
    variables: { threadId },
    skip: !threadId,
  })

  return {
    state: data?.agentState ?? null,
    loading,
    error,
  }
}
