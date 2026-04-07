import { useQuery, gql } from '@apollo/client'
import { useNavigate } from 'react-router'

const THREADS_QUERY = gql`
  query Threads {
    threads {
      id
      name
      createdAt
      archivedAt
    }
  }
`

export function HomePage() {
  const { data, loading } = useQuery(THREADS_QUERY)
  const navigate = useNavigate()

  return (
    <div className="flex h-screen">
      <aside className="w-64 border-r border-border p-4">
        <h2 className="text-lg font-semibold mb-4">Threads</h2>
        {loading && <p className="text-muted">Loading...</p>}
        {data?.threads?.map((thread: { id: string; name: string }) => (
          <button
            key={thread.id}
            onClick={() => navigate(`/thread/${thread.id}`)}
            className="block w-full text-left p-2 rounded hover:bg-secondary mb-1"
          >
            {thread.name || 'Untitled'}
          </button>
        ))}
      </aside>
      <main className="flex-1 flex items-center justify-center">
        <div className="text-center">
          <h1 className="text-4xl font-bold mb-4">Spidey</h1>
          <p className="text-muted">Select a thread or create a new one</p>
        </div>
      </main>
    </div>
  )
}
