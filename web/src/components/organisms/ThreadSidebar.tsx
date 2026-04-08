import { gql } from '@apollo/client'
import { useQuery, useMutation } from '@apollo/client/react'
import { useNavigate, useParams } from 'react-router'
import { ScrollArea } from '@/components/ui/scroll-area'
import { cn } from '@/lib/utils'
import { useThreadStateChanges } from '@/hooks/useThreadState'
import { useTheme } from '@/lib/theme'
import type { Thread } from '@/graphql/generated/types'

const THREADS_QUERY = gql`
  query SidebarThreads($includeArchived: Boolean) {
    threads(includeArchived: $includeArchived) {
      id
      name
      createdAt
      archivedAt
      parentThreadId
      messageCount
    }
  }
`

const CREATE_THREAD = gql`
  mutation CreateThread($name: String) {
    createThread(name: $name) { id name }
  }
`

const ARCHIVE_THREAD = gql`
  mutation ArchiveThread($id: ID!) { archiveThread(id: $id) }
`

const DELETE_THREAD = gql`
  mutation DeleteThread($id: ID!) { deleteThread(id: $id) }
`

type ThreadsData = { threads: Thread[] }

function formatRelativeTime(dateStr: string): string {
  const date = new Date(dateStr)
  const now = new Date()
  const diffMs = now.getTime() - date.getTime()
  const diffMin = Math.floor(diffMs / 60000)
  if (diffMin < 1) return 'now'
  if (diffMin < 60) return `${diffMin}m`
  const diffHr = Math.floor(diffMin / 60)
  if (diffHr < 24) return `${diffHr}h`
  const diffDays = Math.floor(diffHr / 24)
  if (diffDays < 7) return `${diffDays}d`
  return date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

function threadDisplayName(thread: Thread): string {
  if (thread.name && thread.name !== 'New Thread' && thread.name !== 'Untitled') {
    return thread.name
  }
  return 'New conversation'
}

export function ThreadSidebar() {
  const { threadId } = useParams<{ threadId: string }>()
  const navigate = useNavigate()
  const { data, loading, refetch } = useQuery<ThreadsData>(THREADS_QUERY, {
    variables: { includeArchived: false },
  })
  const [createThread] = useMutation<{createThread: {id: string; name: string}}>(CREATE_THREAD)
  const [archiveThread] = useMutation(ARCHIVE_THREAD)
  const [deleteThread] = useMutation(DELETE_THREAD)
  const { resolved, setTheme } = useTheme()
  const { event: stateChange } = useThreadStateChanges()
  // Refetch thread list when any thread state changes
  if (stateChange) {
    refetch()
  }

  const handleNew = async () => {
    const result = await createThread({ variables: { name: null } })
    if (result.data?.createThread) {
      refetch()
      navigate(`/thread/${result.data.createThread.id}`)
    }
  }

  const handleArchive = async (id: string, e: React.MouseEvent) => {
    e.stopPropagation()
    await archiveThread({ variables: { id } })
    refetch()
  }

  const handleDelete = async (id: string, e: React.MouseEvent) => {
    e.stopPropagation()
    if (!confirm('Delete this thread permanently?')) return
    await deleteThread({ variables: { id } })
    refetch()
    if (threadId === id) navigate('/')
  }

  const threads = data?.threads ?? []

  return (
    <aside className="sidebar w-64 flex flex-col shrink-0">
      {/* Header */}
      <div className="h-14 px-4 flex items-center shrink-0">
        <button
          onClick={() => navigate('/')}
          className="text-base font-semibold tracking-tight text-foreground/80 hover:text-foreground transition-colors"
        >
          Spidey
        </button>
      </div>

      {/* New thread button */}
      <div className="px-3 pb-3">
        <button
          onClick={handleNew}
          className="w-full flex items-center gap-2 px-3 py-2.5 rounded-xl text-sm text-muted-foreground hover:text-foreground hover:bg-foreground/[0.04] transition-all"
        >
          <span className="text-lg leading-none">+</span>
          <span>New thread</span>
        </button>
      </div>

      {/* Thread list */}
      <ScrollArea className="flex-1">
        <div className="px-2 space-y-0.5">
          {loading && <p className="text-xs text-muted-foreground px-3 py-2">Loading...</p>}
          {threads.map((thread) => {
            const isActive = threadId === thread.id
            const isBranch = !!thread.parentThreadId
            return (
              <button
                key={thread.id}
                className={cn(
                  'group flex items-center w-full text-left px-3 py-2.5 rounded-xl transition-all relative',
                  isActive
                    ? 'sidebar-item-active text-foreground'
                    : 'text-muted-foreground hover:text-foreground hover:bg-foreground/[0.03]',
                  isBranch && 'ml-4',
                )}
                onClick={() => navigate(`/thread/${thread.id}`)}
              >
                {isBranch && (
                  <span className="text-[10px] text-muted-foreground/40 mr-1.5">&#8627;</span>
                )}
                <span className={cn(
                  'truncate flex-1 text-[13px] leading-snug',
                  isActive && 'font-medium text-foreground',
                )}>
                  {threadDisplayName(thread)}
                </span>
                <span className="text-[10px] text-muted-foreground/40 shrink-0 tabular-nums ml-2 group-hover:hidden">
                  {(thread.messageCount ?? 0) > 0
                    ? `${thread.messageCount}`
                    : thread.createdAt ? formatRelativeTime(thread.createdAt) : ''}
                </span>

                {/* Hover actions */}
                <span className="hidden group-hover:flex gap-0.5 shrink-0 ml-2">
                  <button
                    onClick={(e) => handleArchive(thread.id, e)}
                    className="h-5 w-5 rounded-md flex items-center justify-center text-muted-foreground/60 hover:text-foreground hover:bg-foreground/[0.06] transition-colors"
                    title="Archive"
                  >
                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M21 8v13H3V8M1 3h22v5H1zM10 12h4"/></svg>
                  </button>
                  <button
                    onClick={(e) => handleDelete(thread.id, e)}
                    className="h-5 w-5 rounded-md flex items-center justify-center text-muted-foreground/60 hover:text-destructive hover:bg-destructive/10 transition-colors"
                    title="Delete"
                  >
                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M3 6h18M8 6V4h8v2M19 6v14a2 2 0 01-2 2H7a2 2 0 01-2-2V6"/></svg>
                  </button>
                </span>
              </button>
            )
          })}
        </div>
      </ScrollArea>

      {/* Footer */}
      <div className="p-3 flex items-center gap-1">
        <button
          onClick={() => navigate('/settings')}
          className="flex items-center gap-2 flex-1 px-3 py-2 rounded-xl text-xs text-muted-foreground/60 hover:text-muted-foreground hover:bg-foreground/[0.03] transition-all"
        >
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 010 2.83 2 2 0 01-2.83 0l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-4 0v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 01-2.83-2.83l.06-.06A1.65 1.65 0 004.68 15a1.65 1.65 0 00-1.51-1H3a2 2 0 010-4h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 012.83-2.83l.06.06A1.65 1.65 0 009 4.68a1.65 1.65 0 001-1.51V3a2 2 0 014 0v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 012.83 2.83l-.06.06A1.65 1.65 0 0019.4 9a1.65 1.65 0 001.51 1H21a2 2 0 010 4h-.09a1.65 1.65 0 00-1.51 1z"/></svg>
          Settings
        </button>
        <button
          onClick={() => setTheme(resolved === 'dark' ? 'light' : 'dark')}
          className="h-8 w-8 rounded-xl flex items-center justify-center text-muted-foreground/60 hover:text-muted-foreground hover:bg-foreground/[0.03] transition-all shrink-0"
          title={`Switch to ${resolved === 'dark' ? 'light' : 'dark'} mode`}
        >
          {resolved === 'dark' ? (
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><circle cx="12" cy="12" r="5"/><line x1="12" y1="1" x2="12" y2="3"/><line x1="12" y1="21" x2="12" y2="23"/><line x1="4.22" y1="4.22" x2="5.64" y2="5.64"/><line x1="18.36" y1="18.36" x2="19.78" y2="19.78"/><line x1="1" y1="12" x2="3" y2="12"/><line x1="21" y1="12" x2="23" y2="12"/><line x1="4.22" y1="19.78" x2="5.64" y2="18.36"/><line x1="18.36" y1="5.64" x2="19.78" y2="4.22"/></svg>
          ) : (
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M21 12.79A9 9 0 1111.21 3 7 7 0 0021 12.79z"/></svg>
          )}
        </button>
      </div>
    </aside>
  )
}
