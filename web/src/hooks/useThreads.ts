import { useMemo } from 'react'
import { useApolloClient, useQuery, useSubscription } from '@apollo/client/react'
import { LIST_THREADS, THREAD_STATE_CHANGES } from '@/graphql/operations'
import { adaptThread } from '@/domain/adapters'
import type {
  ListThreadsQuery,
  ListThreadsQueryVariables,
  ThreadStateChangesSubscription,
  ThreadStateChangesSubscriptionVariables,
} from '@/graphql/generated/types'
import type { Thread } from '@/domain/types'

export interface ThreadsResult {
  threads: Thread[]
  loading: boolean
  error: string | null
}

/** Subscribes to threadStateChanges and patches the cached
 *  LIST_THREADS results so both `includeArchived` variants
 *  re-render without a refetch. status / mode / name now flow
 *  through the Apollo cache directly — Thread carries those
 *  fields on the schema, so the sidebar's running tick is
 *  whatever Apollo last saw, not a parallel module-scope state
 *  map. */
function useLiveThreadStatePatching() {
  const client = useApolloClient()
  useSubscription<ThreadStateChangesSubscription, ThreadStateChangesSubscriptionVariables>(
    THREAD_STATE_CHANGES,
    {
      onData: ({ data }) => {
        const ev = data.data?.threadStateChanges
        if (!ev) return
        for (const v of [{ includeArchived: true }, { includeArchived: false }]) {
          const cached = client.readQuery<ListThreadsQuery>({
            query: LIST_THREADS,
            variables: v,
          })
          if (!cached?.threads) continue
          // Backend publishes threadStateChanges with only threadId
          // populated on archive / unarchive / delete — name /
          // status / mode arrive as zero values. Patch only fields
          // that actually carry new info so we don't briefly
          // rename threads to "" before the refetch lands.
          const next = cached.threads.map((t) => {
            if (t.id !== ev.threadId) return t
            const patch: Partial<typeof t> = {}
            if (ev.name) patch.name = ev.name
            if (ev.status) patch.status = ev.status
            if (ev.mode) patch.mode = ev.mode
            return Object.keys(patch).length > 0 ? { ...t, ...patch } : t
          })
          if (next.some((t) => t.id === ev.threadId)) {
            client.writeQuery({
              query: LIST_THREADS,
              variables: v,
              data: { threads: next },
            })
          }
        }
      },
    },
  )
}

export function useThreads(includeArchived = true): ThreadsResult {
  const { data, loading, error } = useQuery<ListThreadsQuery, ListThreadsQueryVariables>(
    LIST_THREADS,
    { variables: { includeArchived } },
  )
  useLiveThreadStatePatching()

  const threads = useMemo<Thread[]>(
    () => (data?.threads ? data.threads.map(adaptThread) : []),
    [data],
  )

  return { threads, loading, error: error?.message ?? null }
}
