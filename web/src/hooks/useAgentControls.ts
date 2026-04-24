import { useCallback } from 'react'
import { useMutation } from '@apollo/client/react'
import { PAUSE_AGENT, RESUME_AGENT, STOP_AGENT } from '@/graphql/operations'
import type {
  PauseAgentMutation,
  PauseAgentMutationVariables,
  ResumeAgentMutation,
  ResumeAgentMutationVariables,
  StopAgentMutation,
  StopAgentMutationVariables,
} from '@/graphql/generated/types'

export interface AgentControls {
  stop: (threadId: string) => Promise<void>
  pause: (threadId: string) => Promise<void>
  resume: (threadId: string, correction?: string) => Promise<void>
  pending: boolean
  error: string | null
}

export function useAgentControls(): AgentControls {
  const [stopMut, stopRes] = useMutation<StopAgentMutation, StopAgentMutationVariables>(
    STOP_AGENT,
    { refetchQueries: ['GetAgentState'] },
  )
  const [pauseMut, pauseRes] = useMutation<PauseAgentMutation, PauseAgentMutationVariables>(
    PAUSE_AGENT,
    { refetchQueries: ['GetAgentState'] },
  )
  const [resumeMut, resumeRes] = useMutation<ResumeAgentMutation, ResumeAgentMutationVariables>(
    RESUME_AGENT,
    { refetchQueries: ['GetAgentState'] },
  )

  const stop = useCallback(async (threadId: string) => {
    await stopMut({ variables: { threadId } })
  }, [stopMut])
  const pause = useCallback(async (threadId: string) => {
    await pauseMut({ variables: { threadId } })
  }, [pauseMut])
  const resume = useCallback(
    async (threadId: string, correction?: string) => {
      await resumeMut({ variables: { threadId, correction: correction ?? null } })
    },
    [resumeMut],
  )

  return {
    stop,
    pause,
    resume,
    pending: stopRes.loading || pauseRes.loading || resumeRes.loading,
    error: stopRes.error?.message ?? pauseRes.error?.message ?? resumeRes.error?.message ?? null,
  }
}
