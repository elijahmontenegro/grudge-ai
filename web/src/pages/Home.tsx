import { gql } from '@apollo/client'
import { useMutation } from '@apollo/client/react'
import { useNavigate } from 'react-router'
import { ThreadSidebar } from '@/components/organisms/ThreadSidebar'

const CREATE_THREAD = gql`
  mutation HomeCreateThread($name: String) {
    createThread(name: $name) { id name }
  }
`

export function HomePage() {
  const navigate = useNavigate()
  const [createThread] = useMutation<{createThread: {id: string; name: string}}>(CREATE_THREAD)

  const handleNew = async () => {
    const result = await createThread({ variables: { name: null } })
    if (result.data?.createThread) {
      navigate(`/thread/${result.data.createThread.id}`)
    }
  }

  return (
    <div className="flex h-screen">
      <ThreadSidebar />
      <main className="flex-1 min-w-0 flex items-center justify-center p-8">
        <div className="w-full max-w-xl">
          <h1 className="text-4xl font-bold tracking-tight leading-[1.1] mb-3">
            Spidey
          </h1>
          <p className="text-base text-muted-foreground/40 leading-relaxed mb-8">
            RRC-native agentic framework. Every message scored,
            every prerequisite tracked, every response assembled
            from what actually matters.
          </p>

          <button onClick={handleNew} className="composer w-full text-left cursor-pointer group">
            <div className="px-5 py-4 flex items-center justify-between">
              <span className="text-base text-muted-foreground/25 group-hover:text-muted-foreground/40 transition-colors">
                New thread...
              </span>
              <span className="h-9 w-9 rounded-xl bg-primary/10 flex items-center justify-center text-primary group-hover:bg-primary group-hover:text-primary-foreground transition-all">
                <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
              </span>
            </div>
          </button>

          <p className="mt-6 text-[11px] text-muted-foreground/15">
            <kbd className="px-1.5 py-0.5 bg-foreground/[0.03] rounded text-[10px] font-mono">Ctrl+K</kbd>
            {' '}search
            <span className="mx-2 text-muted-foreground/10">·</span>
            <kbd className="px-1.5 py-0.5 bg-foreground/[0.03] rounded text-[10px] font-mono">Ctrl+N</kbd>
            {' '}new thread
          </p>
        </div>
      </main>
    </div>
  )
}
