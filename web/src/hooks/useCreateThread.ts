import { useCallback } from 'react'
import { useMutation } from '@apollo/client/react'
import { CREATE_THREAD } from '@/graphql/operations'
import type {
  CreateThreadMutation,
  CreateThreadMutationVariables,
} from '@/graphql/generated/types'

export interface CreateThreadResult {
  create: (name?: string, workingDirs?: string[], sandboxed?: boolean) => Promise<string | null>
  loading: boolean
  error: string | null
}

export function useCreateThread(): CreateThreadResult {
  const [run, { loading, error }] = useMutation<CreateThreadMutation, CreateThreadMutationVariables>(
    CREATE_THREAD,
    // Refetch by operation name so every active ListThreads observer
    // (Home uses includeArchived=false, Sidebar uses true) gets updated.
    { refetchQueries: ['ListThreads'] },
  )

  const create = useCallback(
    async (name?: string, workingDirs?: string[], sandboxed?: boolean) => {
      try {
        const res = await run({
          variables: {
            name: name ?? null,
            workingDirs: workingDirs ?? null,
            sandboxed: sandboxed ?? null,
          },
        })
        return res.data?.createThread.id ?? null
      } catch {
        return null
      }
    },
    [run],
  )

  return { create, loading, error: error?.message ?? null }
}
