import { ThreadSidebar } from '@/components/organisms/ThreadSidebar'

export function HomePage() {
  return (
    <div className="flex h-screen">
      <ThreadSidebar />
      <main className="flex-1 flex items-center justify-center">
        <div className="text-center space-y-4">
          <h1 className="text-4xl font-bold tracking-tight">Spidey</h1>
          <p className="text-muted text-sm">Select a thread or create a new one</p>
          <p className="text-xs text-muted">
            Press <kbd className="px-1.5 py-0.5 bg-secondary rounded text-[10px] font-mono">⌘K</kbd> to search
          </p>
        </div>
      </main>
    </div>
  )
}
