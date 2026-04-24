import type { Thread } from '@/data/types'

// Each bar is uniform — the glyph is a density texture keyed off msgCount,
// not a fake sparkline of per-message role/length. Using data-role and
// randomized heights (as earlier iterations did) implied the bars reflected
// real corpus structure, which they did not.
const N = 40

export function CorpusCard({ thread, onOpen }: { thread: Thread; onOpen: () => void }) {
  const count = thread.msgCount || 0
  const filled = Math.min(N, Math.round((count / 50) * N))
  const bars: React.ReactNode[] = []
  for (let i = 0; i < N; i++) {
    bars.push(<i key={i} data-filled={i < filled ? 'true' : undefined} />)
  }

  const autonomous = (thread.elapsed || '').startsWith('autonomous')
  return (
    <div className="corpus-card" onClick={onOpen}>
      <div className="corpus-glyph">{bars}</div>
      <div className="corpus-card-body">
        <div className="name">{thread.name}</div>
        <div className="meta">
          {thread.msgCount} messages
          {thread.lastActive && <> · active {thread.lastActive}</>}
          {autonomous && (
            <>
              {' '}
              · <span style={{ color: 'var(--accent-ink)' }}>{thread.elapsed}</span>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
