import { useCallback } from 'react'
import { useMutation } from '@apollo/client/react'
import {
  PAUSE_AGENT,
  RESUME_AGENT,
  START_AUTONOMOUS,
  STOP_AGENT,
} from '@/graphql/operations'
import type {
  PauseAgentMutation,
  PauseAgentMutationVariables,
  ResumeAgentMutation,
  ResumeAgentMutationVariables,
  StartAutonomousMutation,
  StartAutonomousMutationVariables,
  StopAgentMutation,
  StopAgentMutationVariables,
} from '@/graphql/generated/types'
import type { AttachmentMeta } from './useAttachments'

export interface AgentControls {
  start: (
    threadId: string,
    prompt: string,
    duration: string,
    attachments?: AttachmentMeta[],
  ) => Promise<void>
  stop: (threadId: string) => Promise<void>
  pause: (threadId: string) => Promise<void>
  resume: (threadId: string, correction?: string) => Promise<void>
  pending: boolean
  error: string | null
}

export function useAgentControls(): AgentControls {
  const [startMut, startRes] = useMutation<
    StartAutonomousMutation,
    StartAutonomousMutationVariables
  >(START_AUTONOMOUS, { refetchQueries: ['GetAgentState'] })
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

  const start = useCallback(
    async (
      threadId: string,
      prompt: string,
      duration: string,
      attachments?: AttachmentMeta[],
    ) => {
      await startMut({ variables: { threadId, prompt, duration, attachments } })
    },
    [startMut],
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
    start,
    stop,
    pause,
    resume,
    pending:
      startRes.loading ||
      stopRes.loading ||
      pauseRes.loading ||
      resumeRes.loading,
    error:
      startRes.error?.message ??
      stopRes.error?.message ??
      pauseRes.error?.message ??
      resumeRes.error?.message ??
      null,
  }
}
