import { useQuery, gql } from '@apollo/client'
import { useNavigate } from 'react-router'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'
import { Badge } from '@/components/ui/badge'

const THREADS_QUERY = gql`
  query Threads($includeArchived: Boolean) {
    threads(includeArchived: $includeArchived) {
      id
      name
      createdAt
      archivedAt
    }
  }
`

export function HomePage() {
  const { data, loading } = useQuery(THREADS_QUERY, {
    variables: { includeArchived: false },
  })
  const navigate = useNavigate()

  return (
    <div className="flex h-screen">
      <aside className="w-72 border-r border-border flex flex-col">
        <div className="p-4 flex items-center justify-between">
          <h2 className="text-lg font-semibold">Threads</h2>
          <Button size="sm" variant="outline">New</Button>
        </div>
        <Separator />
        <ScrollArea className="flex-1">
          <div className="p-2">
            {loading && <p className="text-sm text-muted p-2">Loading...</p>}
            {data?.threads?.map((thread: { id: string; name: string; archivedAt: string | null }) => (
              <button
                key={thread.id}
                onClick={() => navigate(`/thread/${thread.id}`)}
                className="flex items-center gap-2 w-full text-left p-2 rounded-md hover:bg-secondary text-sm"
              >
                <span className="truncate flex-1">{thread.name || 'Untitled'}</span>
                {thread.archivedAt && <Badge variant="secondary">Archived</Badge>}
              </button>
            ))}
          </div>
        </ScrollArea>
      </aside>
      <main className="flex-1 flex items-center justify-center">
        <div className="text-center space-y-4">
          <h1 className="text-4xl font-bold tracking-tight">Spidey</h1>
          <p className="text-muted">Select a thread or create a new one</p>
          <Button onClick={() => {}}>New Thread</Button>
        </div>
      </main>
    </div>
  )
}
