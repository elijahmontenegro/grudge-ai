import { useApolloClient, useQuery, useSubscription } from '@apollo/client/react'
import { LIST_THREADS, THREAD_STATE_CHANGES } from '@/graphql/operations'
import type {
  ListThreadsQuery,
  ListThreadsQueryVariables,
  ThreadStateChangesSubscription,
  ThreadStateChangesSubscriptionVariables,
} from '@/graphql/generated/types'

export type ThreadSummary = ListThreadsQuery['threads'][number]

export interface ThreadsResult {
  threads: ThreadSummary[]
  loading: boolean
  error: string | null
}

/** Subscribes to threadStateChanges and patches the normalised
 *  Thread entity in the Apollo cache. Every observer of the same
 *  Thread (sidebar list, topbar, palette hits) sees the change
 *  through the entity update — Apollo broadcasts to every active
 *  query selecting that Thread by id.
 *
 *  Backend publishes name / status / mode independently; only
 *  fields that carry new info enter the patch so we don't briefly
 *  rename a thread to "" or flip its status to IDLE because the
 *  event wasn't carrying that field.
 *
 *  Archive / unarchive / delete shape list membership rather than
 *  entity fields, and the LIST_THREADS query is filtered server-
 *  side by includeArchived. Apollo can't know to add or remove a
 *  thread from those filtered list views via cache.modify alone,
 *  so consumers refetch LIST_THREADS via refetchQueries on those
 *  mutations — this hook stays focused on entity-level changes. */
function useLiveThreadStatePatching() {
  const client = useApolloClient()
  useSubscription<ThreadStateChangesSubscription, ThreadStateChangesSubscriptionVariables>(
    THREAD_STATE_CHANGES,
    {
      onData: ({ data }) => {
        const ev = data.data?.threadStateChanges
        if (!ev) return
        const id = client.cache.identify({ __typename: 'Thread', id: ev.threadId })
        if (!id) return
        const fields: Record<string, () => unknown> = {}
        if (ev.name) fields.name = () => ev.name
        if (ev.status) fields.status = () => ev.status
        if (ev.mode) fields.mode = () => ev.mode
        if (Object.keys(fields).length === 0) return
        client.cache.modify({ id, fields })
      },
    },
  )
}

/** Returns the gql ListThreads payload directly. Consumers use
 *  helpers in @/domain/derive for computed fields (lastActive,
 *  state, archived predicate). */
export function useThreads(includeArchived = true): ThreadsResult {
  const { data, loading, error } = useQuery<ListThreadsQuery, ListThreadsQueryVariables>(
    LIST_THREADS,
    { variables: { includeArchived } },
  )
  useLiveThreadStatePatching()
  return {
    threads: data?.threads ? [...data.threads] : [],
    loading,
    error: error?.message ?? null,
  }
}
