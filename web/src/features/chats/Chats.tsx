import { useMemo, useState } from 'react'
import { ThreadRow } from '@/features/chrome/ThreadRow'
import { IconSearch, IconX } from '@/primitives/icons'
import { useThreads, type ThreadSummary } from '@/hooks/useThreads'
import { useRecentActivity } from '@/hooks/useRecentActivity'
import { useThreadMutations } from '@/hooks/useThreadMutations'

type Filter = 'all' | 'starred' | 'archived'

interface ChatsProps {
  onOpenThread: (id: string) => void
}

/**
 * Full chat browser. The sidebar's capped Recent list makes this the place
 * where every thread is reachable, with filters (All / Starred / Archived)
 * and a parallel activity feed so the user can scan "what happened where"
 * without opening threads one by one.
 *
 * Home remains the composer-first landing — this page is the list/archive
 * surface. Keeping them separate avoids the tension of "is home a
 * dashboard or a prompt entry?"
 */
export function Chats({ onOpenThread }: ChatsProps) {
  const [filter, setFilter] = useState<Filter>('all')
  const [query, setQuery] = useState('')
  const { threads, loading, error } = useThreads(true /* include archived */)
  const { items: activity, loading: activityLoading } = useRecentActivity(24)
  const muts = useThreadMutations()

  const shown = useMemo<ThreadSummary[]>(() => {
    const q = query.trim().toLowerCase()
    return threads
      .filter((t) => !t.parentThreadId) // roots only — branches sit under parents
      .filter((t) => {
        const archived = !!t.archivedAt
        if (filter === 'starred') return false // pinned not on schema; future
        if (filter === 'archived') return archived
        return !archived
      })
      .filter((t) => !q || t.name.toLowerCase().includes(q) || t.id.toLowerCase().includes(q))
  }, [threads, filter, query])

  const counts = useMemo(() => {
    const roots = threads.filter((t) => !t.parentThreadId)
    return {
      all: roots.filter((t) => !t.archivedAt).length,
      starred: 0,
      archived: roots.filter((t) => !!t.archivedAt).length,
    }
  }, [threads])

  return (
    <div className="chats-page">
      <header className="chats-head">
        <h1>Threads</h1>
        <div className="chats-head-meta">
          {loading ? 'loading…' : error ? <span style={{ color: 'var(--danger)' }}>{error}</span> : `${counts.all + counts.archived} total`}
        </div>
      </header>

      <div className="chats-body">
        <section className="chats-col chats-col-list">
          <div className="chats-filter-row">
            <div className="chats-filters">
              <button data-on={filter === 'all'} onClick={() => setFilter('all')}>
                all <span>{counts.all}</span>
              </button>
              <button data-on={filter === 'starred'} onClick={() => setFilter('starred')}>
                starred <span>{counts.starred}</span>
              </button>
              <button data-on={filter === 'archived'} onClick={() => setFilter('archived')}>
                archived <span>{counts.archived}</span>
              </button>
            </div>
            <div className="chats-search">
              <IconSearch size={13} />
              <input
                type="text"
                placeholder="Filter"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
              {query && (
                <button onClick={() => setQuery('')} title="Clear">
                  <IconX size={10} />
                </button>
              )}
            </div>
          </div>
          <div className="chats-list scroll">
            {shown.length === 0 && (
              <div className="chats-empty">
                {query
                  ? 'no threads match.'
                  : filter === 'archived'
                    ? 'no archived threads.'
                    : filter === 'starred'
                      ? 'no starred threads yet — click the star on any row.'
                      : 'no threads yet. start one from the sidebar.'}
              </div>
            )}
            {shown.map((t) => (
              <ThreadRow
                key={t.id}
                thread={t}
                active={false}
                onClick={() => onOpenThread(t.id)}
                onArchiveToggle={() =>
                  t.archivedAt ? void muts.unarchive(t.id) : void muts.archive(t.id)
                }
                onDelete={() => void muts.remove(t.id)}
                onRename={(n) => void muts.rename(t.id, n)}
              />
            ))}
          </div>
        </section>

        <section className="chats-col chats-col-activity">
          <div className="chats-col-head">
            <span className="chats-col-title">Activity</span>
            <span className="chats-col-sub">latest across all threads</span>
          </div>
          <div className="chats-activity scroll">
            {activityLoading && <div className="chats-empty">loading…</div>}
            {!activityLoading && activity.length === 0 && (
              <div className="chats-empty">nothing recent.</div>
            )}
            {activity.map((a, i) => (
              <button
                key={i}
                className="chats-activity-row"
                onClick={() => a.threadId && onOpenThread(a.threadId)}
                disabled={!a.threadId}
                title={`${a.threadName ?? ''} — ${a.when}`}
              >
                <div className="chats-activity-head">
                  <span className="chats-activity-thread">{a.threadName ?? 'Untitled'}</span>
                  <span className="chats-activity-time">{a.when}</span>
                </div>
                <div className="chats-activity-snippet">{a.what}</div>
              </button>
            ))}
          </div>
        </section>
      </div>
    </div>
  )
}
