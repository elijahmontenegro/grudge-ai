import type { Message, SelectionItem } from '@/data/types'

interface PrereqMarkerProps {
  n: number
  msg: Message | null
  sel: SelectionItem
  onOpen: (id: string, sel: SelectionItem) => void
}

export function PrereqMarker({ n, msg, sel, onOpen }: PrereqMarkerProps) {
  const crossThread = sel.crossThread
  const score = sel.score ?? 0
  const strength = score > 0.85 ? 'strong' : score > 0.6 ? 'mid' : 'weak'
  const label = crossThread
    ? `↗ from ${sel.threadName}: "${sel.snippet}"`
    : `#${msg?.pos}: ${msg?.text || ''}`

  return (
    <button
      className="prq-mark"
      data-strength={strength}
      data-cross={crossThread || undefined}
      title={label}
      onClick={() => onOpen(msg?.id || sel.id, sel)}
    >
      <span className="prq-mark-n">{n}</span>
    </button>
  )
}
