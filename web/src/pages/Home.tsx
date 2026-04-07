import { gql } from '@apollo/client'
import { useQuery } from '@apollo/client/react'
import { ThreadSidebar } from '@/components/organisms/ThreadSidebar'
import { Badge } from '@/components/ui/badge'
import { WarmthIndicator } from '@/components/atoms/WarmthIndicator'
import type { Thread } from '@/graphql/generated/types'

const THREADS_QUERY = gql`
  query HomeThreads {
    threads {
      id
      name
      createdAt
      archivedAt
    }
  }
`

type HomeData = { threads: Thread[] }

export function HomePage() {
  const { data } = useQuery<HomeData>(THREADS_QUERY)
  const threads = data?.threads ?? []

  return (
    <div className="flex h-screen">
      <ThreadSidebar />
      <main className="flex-1 flex flex-col items-center justify-center p-8">
        <div className="text-center space-y-4 max-w-md">
          <h1 className="text-4xl font-bold tracking-tight">Spidey</h1>
          <p className="text-muted text-sm">RRC-native agentic framework</p>
          <p className="text-xs text-muted">
            Press <kbd className="px-1.5 py-0.5 bg-secondary rounded text-[10px] font-mono">&#8984;K</kbd> to search
          </p>
        </div>

        {threads.length > 0 && (
          <div className="mt-8 w-full max-w-md space-y-2">
            <h2 className="text-xs font-semibold text-muted uppercase tracking-wider">Recent threads</h2>
            {threads.slice(0, 5).map((thread) => (
              <a
                key={thread.id}
                href={`/thread/${thread.id}`}
                className="flex items-center gap-2 p-2 rounded-md hover:bg-secondary text-sm"
              >
                <WarmthIndicator warmth={0} />
                <span className="flex-1 truncate">{thread.name || 'Untitled'}</span>
                {thread.archivedAt && <Badge variant="secondary" className="text-[8px]">Archived</Badge>}
              </a>
            ))}
          </div>
        )}
      </main>
    </div>
  )
}
