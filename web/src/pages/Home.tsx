import { gql } from '@apollo/client'
import { useQuery, useMutation } from '@apollo/client/react'
import { useNavigate } from 'react-router'
import { ThreadSidebar } from '@/components/organisms/ThreadSidebar'
import type { Thread } from '@/graphql/generated/types'

const THREADS_QUERY = gql`
  query HomeThreads {
    threads {
      id
      name
      createdAt
      archivedAt
      messageCount
    }
  }
`

const CREATE_THREAD = gql`
  mutation HomeCreateThread($name: String) {
    createThread(name: $name) { id name }
  }
`

type HomeData = { threads: Thread[] }

export function HomePage() {
  const navigate = useNavigate()
  const { data } = useQuery<HomeData>(THREADS_QUERY, { pollInterval: 5000 })
  const [createThread] = useMutation<{createThread: {id: string; name: string}}>(CREATE_THREAD)
  const threads = (data?.threads ?? []).filter((t) => !t.archivedAt)
  const totalMessages = threads.reduce((sum, t) => sum + (t.messageCount ?? 0), 0)
  const activeThreads = threads.filter(t => (t.messageCount ?? 0) > 5)

  const handleNew = async () => {
    const result = await createThread({ variables: { name: null } })
    if (result.data?.createThread) {
      navigate(`/thread/${result.data.createThread.id}`)
    }
  }

  return (
    <div className="flex h-screen">
      <ThreadSidebar />
      <main className="flex-1 min-w-0 overflow-auto">
        <div className="grid grid-cols-[1fr_280px] gap-12 items-start px-12 pt-20">
          {/* Left — hero + composer */}
          <div>
            <h1 className="text-5xl font-bold tracking-tight leading-[1.1] mb-4">
              Spidey
            </h1>
            <p className="text-base text-muted-foreground/50 leading-relaxed mb-10 max-w-md">
              RRC-native agentic framework. Every message scored,
              every prerequisite tracked, every response assembled
              from what actually matters.
            </p>

            <button onClick={handleNew} className="composer w-full text-left cursor-pointer group">
              <div className="px-6 py-5 flex items-center justify-between">
                <span className="text-lg text-muted-foreground/25 group-hover:text-muted-foreground/40 transition-colors">
                  New thread...
                </span>
                <span className="h-10 w-10 rounded-xl bg-primary/10 flex items-center justify-center text-primary group-hover:bg-primary group-hover:text-primary-foreground transition-all group-hover:shadow-[0_0_20px_-4px_rgba(167,139,250,0.4)]">
                  <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
                </span>
              </div>
            </button>

            <div className="mt-8 text-[11px] text-muted-foreground/15">
              <kbd className="px-1.5 py-0.5 bg-foreground/[0.03] rounded text-[10px] font-mono">Ctrl+K</kbd>
              {' '}search
              <span className="mx-2 text-muted-foreground/10">·</span>
              <kbd className="px-1.5 py-0.5 bg-foreground/[0.03] rounded text-[10px] font-mono">Ctrl+N</kbd>
              {' '}new thread
            </div>
          </div>

          {/* Right — corpus stats + active threads */}
          <div className="space-y-6 pt-2">
            <div>
              <div className="text-[10px] text-muted-foreground/30 uppercase tracking-widest mb-3">Corpus</div>
              <div className="space-y-3">
                <div className="flex items-baseline justify-between">
                  <span className="text-sm text-muted-foreground/50">Threads</span>
                  <span className="text-2xl font-bold tabular-nums">{threads.length}</span>
                </div>
                <div className="flex items-baseline justify-between">
                  <span className="text-sm text-muted-foreground/50">Messages</span>
                  <span className="text-2xl font-bold tabular-nums">{totalMessages}</span>
                </div>
                <div className="flex items-baseline justify-between">
                  <span className="text-sm text-muted-foreground/50">Active</span>
                  <span className="text-2xl font-bold tabular-nums text-primary">{activeThreads.length}</span>
                </div>
              </div>
            </div>

            {activeThreads.length > 0 && (
              <div>
                <div className="text-[10px] text-muted-foreground/30 uppercase tracking-widest mb-3">Recent activity</div>
                <div className="space-y-1">
                  {activeThreads.slice(0, 5).map(t => (
                    <button
                      key={t.id}
                      onClick={() => navigate(`/thread/${t.id}`)}
                      className="flex items-center justify-between w-full text-left px-2 py-1.5 rounded-lg hover:bg-foreground/[0.03] transition-colors group"
                    >
                      <span className="text-xs text-muted-foreground/40 group-hover:text-muted-foreground truncate">
                        {t.name && t.name !== 'New Thread' ? t.name : 'conversation'}
                      </span>
                      <span className="text-[10px] text-muted-foreground/25 tabular-nums shrink-0 ml-2">
                        {t.messageCount}
                      </span>
                    </button>
                  ))}
                </div>
              </div>
            )}
          </div>
        </div>
      </main>
    </div>
  )
}
