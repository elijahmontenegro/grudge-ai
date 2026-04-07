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
    createThread(name: $name) {
      id
      name
    }
  }
`

export function ThreadSidebar() {
  const { threadId } = useParams<{ threadId: string }>()
  const navigate = useNavigate()
  const { data, loading, refetch } = useQuery<any>(THREADS_QUERY, {
    variables: { includeArchived: false },
  })
  const [createThread] = useMutation<any>(CREATE_THREAD)
  const { event: stateEvent } = useThreadStateChanges()

  const handleNew = async () => {
    const result = await createThread({ variables: { name: 'New Thread' } })
    if (result.data?.createThread) {
      refetch()
      navigate(`/thread/${result.data.createThread.id}`)
    }
  }

  return (
    <aside className="w-64 border-r border-border flex flex-col bg-card">
      <div className="p-3 flex items-center justify-between">
        <h2 className="text-sm font-semibold">Threads</h2>
        <Button size="sm" variant="ghost" onClick={handleNew}>+</Button>
      </div>
      <Separator />
      <ScrollArea className="flex-1">
        <div className="p-1">
          {loading && <p className="text-xs text-muted p-2">Loading...</p>}
          {data?.threads?.map((thread: {
            id: string
            name: string
            archivedAt: string | null
            parentThreadId: string | null
          }) => (
            <button
              key={thread.id}
              onClick={() => navigate(`/thread/${thread.id}`)}
              className={cn(
                'flex items-center gap-2 w-full text-left px-2 py-1.5 rounded text-sm',
                'hover:bg-secondary',
                threadId === thread.id && 'bg-secondary font-medium',
                thread.parentThreadId && 'ml-3',
              )}
            >
              {thread.parentThreadId && <span className="text-xs text-muted">&#8627;</span>}
              {stateEvent?.threadId === thread.id && (
                <WarmthIndicator warmth={stateEvent.warmth} />
              )}
              <span className="truncate flex-1">{thread.name || 'Untitled'}</span>
              {thread.archivedAt && <Badge variant="secondary" className="text-[10px]">A</Badge>}
            </button>
          ))}
        </div>
      </ScrollArea>
      <Separator />
      <div className="p-2">
        <Button
          variant="ghost"
          size="sm"
          className="w-full text-xs"
          onClick={() => navigate('/settings')}
        >
          Settings
        </Button>
      </div>
    </aside>
  )
}
