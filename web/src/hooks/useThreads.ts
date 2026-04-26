import { useEffect, useMemo, useState } from 'react'
import { useApolloClient, useQuery, useSubscription } from '@apollo/client/react'
import { LIST_THREADS, THREAD_STATE_CHANGES } from '@/graphql/operations'
import { adaptThread } from '@/domain/adapters'
import type {
  ListThreadsQuery,
  ListThreadsQueryVariables,
  ThreadStateChangesSubscription,
  ThreadStateChangesSubscriptionVariables,
} from '@/graphql/generated/types'
import { AgentMode, AgentStatus } from '@/graphql/generated/types'
import type { Thread, ThreadState } from '@/domain/types'

export interface ThreadsResult {
  threads: Thread[]
  loading: boolean
  error: string | null
}

// Module-scope so every useThreads caller sees the same live status map.
// Events are broadcast to all subscribers; we accumulate into this map and
// emit a changed reference so React re-renders.
const liveStatusMap: Map<string, { status: ThreadState; autonomous: boolean }> = new Map()
const liveStatusListeners: Set<() => void> = new Set()
function notifyLiveStatus() {
  for (const l of liveStatusListeners) l()
}

/** Subscribes to threadStateChanges and patches the cached LIST_THREADS
 *  results so both `includeArchived` variants re-render without a refetch.
 *  Also updates a module-scope live-status map that useThreads surfaces so
 *  sidebar rows can show running/paused/autonomous ticks without a per-row
 *  agentState subscription. */
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
          // Backend publishes threadStateChanges with only threadId populated
          // on archive / unarchive / delete — name/status/mode arrive as zero
          // values. Patch only fields that actually carry new info so we
          // don't briefly rename threads to "" before the refetch lands.
          const next = cached.threads.map((t) => {
            if (t.id !== ev.threadId) return t
            const patch: Partial<typeof t> = {}
            if (ev.name) patch.name = ev.name
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
        const status: ThreadState =
          ev.status === AgentStatus.Running
            ? 'running'
            : ev.status === AgentStatus.Paused
              ? 'paused'
              : 'idle'
        liveStatusMap.set(ev.threadId, {
          status,
          autonomous: ev.mode === AgentMode.Autonomous,
        })
        notifyLiveStatus()
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

  // Subscribe to the live-status map so rendered threads pick up status
  // events even though they aren't stored in the Apollo cache.
  const [statusTick, setStatusTick] = useState(0)
  useEffect(() => {
    const listener = () => setStatusTick((n) => n + 1)
    liveStatusListeners.add(listener)
    return () => {
      liveStatusListeners.delete(listener)
    }
  }, [])

  const threads = useMemo<Thread[]>(() => {
    const raw = data?.threads ? data.threads.map(adaptThread) : []
    return raw.map((t) => {
      const live = liveStatusMap.get(t.id)
      if (!live) return t
      return {
        ...t,
        state: live.status,
        elapsed: live.autonomous ? 'autonomous' : t.elapsed,
      }
    })
    // statusTick dep so React recomputes when the live map mutates.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data, statusTick])

  return { threads, loading, error: error?.message ?? null }
}
