import { type RefObject } from 'react'
import type { Message } from '@/data/types'

interface CorpusRailProps {
  corpus: Message[]
  selectedIds: Set<string>
  currentPos?: number
  onJump?: (id: string) => void
  scrollRef: RefObject<HTMLDivElement | null>
}

/**
 * Overlay of message ticks on top of the native scrollbar column. The
 * rail itself has `pointer-events: none` so scrollbar drag / wheel /
 * track-click all route to the browser's native scrollbar underneath
 * (OS-composited, fast). Individual ticks opt into `pointer-events:
 * auto` for click-to-jump on a specific message.
 *
 * Removed from earlier iteration: custom drag-to-scroll, wheel
 * forwarding, viewport thumb. The native scrollbar does all of that
 * natively; re-implementing it in JS gave us a slow drag.
 */
export function CorpusRail({ corpus, selectedIds, currentPos, onJump }: CorpusRailProps) {
  const n = corpus.length || 1
  return (
    <aside className="corpus-rail" title={`corpus · ${corpus.length} messages`}>
      <div className="corpus-rail-track">
        {corpus.map((m, i) => (
          <span
            key={m.id}
            className="corpus-tick"
            style={{ top: `${(i / n) * 100}%` }}
            data-selected={selectedIds.has(m.id) || undefined}
            data-role={m.role}
            data-current={m.pos === currentPos || undefined}
            title={`#${m.pos} · ${m.role}`}
            onClick={(e) => {
              e.stopPropagation()
              onJump?.(m.id)
            }}
          />
        ))}
      </div>
    </aside>
  )
}
