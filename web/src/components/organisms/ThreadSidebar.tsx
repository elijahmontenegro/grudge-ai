import { gql } from '@apollo/client'
import { useQuery, useMutation } from '@apollo/client/react'
import { useNavigate, useParams } from 'react-router'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import { WarmthIndicator } from '@/components/atoms/WarmthIndicator'
import { useThreadStateChanges } from '@/hooks/useThreadState'
import type { Thread } from '@/graphql/generated/types'

const THREADS_QUERY = gql`
  query SidebarThreads($includeArchived: Boolean) {
    threads(includeArchived: $includeArchived) {
      id
      name
      createdAt
      archivedAt
      parentThreadId
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

export function ThreadSidebar() {
  const { threadId } = useParams<{ threadId: string }>()
  const navigate = useNavigate()
  const { data, loading, refetch } = useQuery<ThreadsData>(THREADS_QUERY, {
    variables: { includeArchived: false },
  })
  const [createThread] = useMutation<{createThread: {id: string; name: string}}>(CREATE_THREAD)
  const [archiveThread] = useMutation(ARCHIVE_THREAD)
  const [deleteThread] = useMutation(DELETE_THREAD)
  const { event: stateEvent } = useThreadStateChanges()

  const handleNew = async () => {
    const result = await createThread({ variables: { name: 'New Thread' } })
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
    if (!confirm('Delete thread permanently? This cannot be undone.')) return
    await deleteThread({ variables: { id } })
    refetch()
    if (threadId === id) navigate('/')
  }

  return (
    <aside className="w-64 border-r border-border flex flex-col bg-card">
      <div className="p-3 flex items-center justify-between">
        <h2 className="text-sm font-semibold">Threads</h2>
        <Button size="sm" variant="ghost" onClick={handleNew} className="h-6 w-6 p-0">+</Button>
      </div>
      <Separator />
      <ScrollArea className="flex-1">
        <div className="p-1">
          {loading && <p className="text-xs text-muted p-2">Loading...</p>}
          {data?.threads?.map((thread) => (
            <div
              key={thread.id}
              className={cn(
                'group flex items-center gap-1 w-full text-left px-2 py-1.5 rounded text-sm cursor-pointer',
                'hover:bg-secondary',
                threadId === thread.id && 'bg-secondary font-medium',
                thread.parentThreadId && 'ml-3',
              )}
              onClick={() => navigate(`/thread/${thread.id}`)}
            >
              {thread.parentThreadId && <span className="text-[10px] text-muted">&#8627;</span>}
              {stateEvent?.threadId === thread.id && (
                <WarmthIndicator warmth={stateEvent.warmth} />
              )}
              <span className="truncate flex-1 text-xs">{thread.name || 'Untitled'}</span>
              {thread.archivedAt && <Badge variant="secondary" className="text-[8px]">A</Badge>}
              <span className="opacity-0 group-hover:opacity-100 flex gap-0.5">
                <button
                  onClick={(e) => handleArchive(thread.id, e)}
                  className="text-[10px] text-muted hover:text-foreground"
                  title="Archive"
                >
                  &#128451;
                </button>
                <button
                  onClick={(e) => handleDelete(thread.id, e)}
                  className="text-[10px] text-muted hover:text-destructive"
                  title="Delete"
                >
                  &#10005;
                </button>
              </span>
            </div>
          ))}
        </div>
      </ScrollArea>
      <Separator />
      <div className="p-2">
        <Button
          variant="ghost"
          size="sm"
          className="w-full text-xs h-7"
          onClick={() => navigate('/settings')}
        >
          Settings
        </Button>
      </div>
    </aside>
  )
}
