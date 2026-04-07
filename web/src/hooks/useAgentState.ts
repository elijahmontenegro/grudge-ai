import { useSubscription, gql } from '@apollo/client'

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

export type AgentStatus = 'IDLE' | 'RUNNING' | 'PAUSED'
export type AgentMode = 'NORMAL' | 'AUTONOMOUS' | 'PLAN'

interface AgentState {
  threadId: string
  status: AgentStatus
  mode: AgentMode
  roundCount: number
  startedAt: string | null
  durationLimit: string | null
  elapsedTime: string | null
}

export function useAgentState(threadId: string) {
  const { data, loading, error } = useSubscription<{
    agentState: AgentState
  }>(AGENT_STATE_SUBSCRIPTION, {
    variables: { threadId },
    skip: !threadId,
  })

  return {
    state: data?.agentState ?? null,
    loading,
    error,
  }
}
