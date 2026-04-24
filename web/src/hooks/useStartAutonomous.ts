import { useCallback } from 'react'
import { useMutation } from '@apollo/client/react'
import { START_AUTONOMOUS } from '@/graphql/operations'
import type {
  StartAutonomousMutation,
  StartAutonomousMutationVariables,
} from '@/graphql/generated/types'
import type { AttachmentMeta } from './useAttachments'

export interface StartAutonomousResult {
  start: (
    threadId: string,
    prompt: string,
    duration: string,
    attachments?: AttachmentMeta[],
  ) => Promise<void>
  loading: boolean
  error: string | null
}

export function useStartAutonomous(): StartAutonomousResult {
  const [run, { loading, error }] = useMutation<
    StartAutonomousMutation,
    StartAutonomousMutationVariables
  >(START_AUTONOMOUS, { refetchQueries: ['GetAgentState'] })

  const start = useCallback(
    async (
      threadId: string,
      prompt: string,
      duration: string,
      attachments?: AttachmentMeta[],
    ) => {
      await run({
        variables: { threadId, prompt, duration, attachments },
      })
    },
    [run],
  )

  return { start, loading, error: error?.message ?? null }
}
