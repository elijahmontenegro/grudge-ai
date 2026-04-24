import { useCallback } from 'react'
import { useMutation } from '@apollo/client/react'
import { EDIT_MESSAGE } from '@/graphql/operations'
import type {
  EditMessageMutation,
  EditMessageMutationVariables,
} from '@/graphql/generated/types'

export interface EditMessageResult {
  /** Returns the id of the new branched thread the backend created, or null
   *  on failure. Caller decides whether to navigate. */
  edit: (threadId: string, position: number, newContent: string) => Promise<string | null>
  loading: boolean
  error: string | null
}

/**
 * Single source of truth for editMessage. Hoisted out of `Turn` so a thread
 * with 100 turns doesn't instantiate 100 Apollo mutation hooks — one shared
 * hook at the thread page level is enough.
 */
export function useEditMessage(): EditMessageResult {
  const [run, { loading, error }] = useMutation<
    EditMessageMutation,
    EditMessageMutationVariables
  >(EDIT_MESSAGE, {
    // Name-based refetch — all active ListThreads observers (Home + Sidebar
    // use different includeArchived values).
    refetchQueries: ['ListThreads'],
  })

  const edit = useCallback(
    async (threadId: string, position: number, newContent: string) => {
      const res = await run({
        variables: { threadId, messagePosition: position, newContent },
      })
      return res.data?.editMessage?.id ?? null
    },
    [run],
  )

  return { edit, loading, error: error?.message ?? null }
}
