import { useQuery, useSubscription } from '@apollo/client/react'
import { AGENT_STATE_SUB, GET_AGENT_STATE } from '@/graphql/operations'
import type {
  AgentStateSubSubscription,
  AgentStateSubSubscriptionVariables,
  GetAgentStateQuery,
  GetAgentStateQueryVariables,
} from '@/graphql/generated/types'
import { AgentMode, AgentStatus } from '@/graphql/generated/types'

export interface RetrySummary {
  attempt: number
  maxAttempts: number
  error: string | null
  nextDelayMs: number
  final: boolean
}

export interface AgentStateSummary {
  status: AgentStatus
  mode: AgentMode
  elapsedTime: string | null
  planContent: string | null
  isAutonomous: boolean
  loading: boolean
  error: string | null
  /** Non-null while a transient LLM call is being retried. Cleared
   *  on success or final failure. */
  retry: RetrySummary | null
}

const EMPTY: AgentStateSummary = {
  status: AgentStatus.Idle,
  mode: AgentMode.Normal,
  elapsedTime: null,
  planContent: null,
  isAutonomous: false,
  loading: false,
  error: null,
  retry: null,
}

export function useAgentState(threadId: string): AgentStateSummary {
  const { data, loading, error } = useQuery<GetAgentStateQuery, GetAgentStateQueryVariables>(
    GET_AGENT_STATE,
    { variables: { threadId }, skip: !threadId },
  )
  const { data: subData } = useSubscription<
    AgentStateSubSubscription,
    AgentStateSubSubscriptionVariables
  >(AGENT_STATE_SUB, { variables: { threadId }, skip: !threadId })

  if (!threadId) return EMPTY

  const live = subData?.agentState ?? data?.agentState
  if (!live) {
    return { ...EMPTY, loading, error: error?.message ?? null }
  }

  return {
    status: live.status,
    mode: live.mode,
    elapsedTime: live.elapsedTime ?? null,
    planContent: live.planContent ?? null,
    isAutonomous: live.mode === AgentMode.Autonomous,
    loading: false,
    error: null,
    retry: live.retry
      ? {
          attempt: live.retry.attempt,
          maxAttempts: live.retry.maxAttempts,
          error: live.retry.error ?? null,
          nextDelayMs: live.retry.nextDelayMs,
          final: live.retry.final,
        }
      : null,
  }
}
