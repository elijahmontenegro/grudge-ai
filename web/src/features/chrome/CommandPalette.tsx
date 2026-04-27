import { useEffect, useMemo, useRef, useState } from 'react'
import { IconArrow, IconSearch, IconSettings, IconSkill } from '@/primitives/icons'
import { PALETTE_ITEMS } from '@/data/fixtures'
import { useSearch, type SearchHit } from '@/hooks/useSearch'
import { useSkills } from '@/hooks/useSkills'
import type { ThreadSummary } from '@/hooks/useThreads'
import type { PaletteItem } from '@/domain/types'

interface PaletteProps {
  open: boolean
  onClose: () => void
  onRun: (item: PaletteItem) => void
  threads: ThreadSummary[]
  /** messageId is passed when a search result was clicked, so the target
   *  thread can scroll to that specific turn. Null for direct thread jumps. */
  onOpenThread: (id: string, messageId?: string) => void
  /** Create a new thread and send this text as its opening message. Wired
   *  to the empty-results Enter affordance ("Press ⏎ to start a new
   *  thread with this prompt"). */
  onNewThreadWithPrompt: (prompt: string) => void
}

export function CommandPalette({
  open,
  onClose,
  onRun,
  threads,
  onOpenThread,
  onNewThreadWithPrompt,
}: PaletteProps) {
  const [q, setQ] = useState('')
  const [idx, setIdx] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const { skills } = useSkills()

  useEffect(() => {
    if (open) {
      setQ('')
      setIdx(0)
      setTimeout(() => inputRef.current?.focus(), 30)
    }
  }, [open])

  // Merge static actions/settings with live skills into a single PaletteItem
  // stream. Live skills inherit kind "skill" so the icon + filter treats them
  // identically. Skill descriptions go into `hint`.
  const items = useMemo<PaletteItem[]>(() => {
    const skillItems: PaletteItem[] = skills.map((s) => ({
      kind: 'skill',
      label: s.name,
      hint: s.description || 'skill',
    }))
    // PALETTE_ITEMS only ships static actions + settings now — no fixture
    // skills or threads, so there's nothing to dedupe against.
    return [...PALETTE_ITEMS, ...skillItems]
  }, [skills])

  const filtered = items.filter(
    (i) =>
      i.label.toLowerCase().includes(q.toLowerCase()) ||
      i.kind.toLowerCase().includes(q.toLowerCase()) ||
      i.hint.toLowerCase().includes(q.toLowerCase()),
  )

  const { results: searchHits, loading: searching, error: searchError } = useSearch(q)
  const showSearch = q.trim().length >= 2
  const hits = showSearch ? searchHits : []

  const flatLen = filtered.length + hits.length

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (!open) return
      if (e.key === 'Escape') {
        // Palette is a full-screen modal — consume the Escape so it
        // doesn't also close whatever's underneath (drawer, user menu).
        e.stopPropagation()
        onClose()
      } else if (e.key === 'ArrowDown') {
        e.preventDefault()
        setIdx((i) => Math.min(flatLen - 1, i + 1))
      } else if (e.key === 'ArrowUp') {
        e.preventDefault()
        setIdx((i) => Math.max(0, i - 1))
      } else if (e.key === 'Enter') {
        e.preventDefault()
        if (idx < filtered.length) {
          const item = filtered[idx]
          if (item) runItem(item)
        } else if (idx < filtered.length + hits.length) {
          const hit = hits[idx - filtered.length]
          if (hit) {
            onOpenThread(hit.threadId, hit.messageId)
            onClose()
          }
        } else if (q.trim().length > 0 && flatLen === 0) {
          // Empty results + non-empty query → honor the footer affordance
          // "Press ⏎ to start a new thread with this prompt".
          onNewThreadWithPrompt(q.trim())
          onClose()
        }
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, filtered, hits, idx, flatLen, onClose])

  function runItem(item: PaletteItem) {
    if (item.kind === 'thread') {
      const t = threads.find((x) => x.name === item.label)
      if (t) {
        onOpenThread(t.id)
        onClose()
        return
      }
    }
    onRun(item)
    onClose()
  }

  if (!open) return null

  const grouped: Record<string, PaletteItem[]> = {}
  filtered.forEach((i) => (grouped[i.kind] ||= []).push(i))

  let runningIdx = -1
  return (
    <div className="palette-overlay" onClick={onClose}>
      <div className="palette" onClick={(e) => e.stopPropagation()}>
        <input
          ref={inputRef}
          className="palette-input"
          placeholder="Search, run a command, switch thread, invoke a skill…"
          value={q}
          onChange={(e) => {
            setQ(e.target.value)
            setIdx(0)
          }}
        />
        <div className="palette-results scroll">
          {Object.entries(grouped).map(([kind, kindItems]) => (
            <div key={kind}>
              <div className="palette-group-head">{kind === 'thread' ? 'threads' : kind}</div>
              {kindItems.map((item) => {
                runningIdx++
                const sel = runningIdx === idx
                const currentIdx = runningIdx
                return (
                  <div
                    key={`${item.kind}:${item.label}`}
                    className="palette-item"
                    data-sel={sel}
                    onMouseEnter={() => setIdx(currentIdx)}
                    onClick={() => runItem(item)}
                  >
                    <span className="icon">
                      {item.kind === 'action' && <IconArrow />}
                      {item.kind === 'thread' && <span className="thread-dot" data-state="idle" />}
                      {item.kind === 'skill' && <IconSkill />}
                      {item.kind === 'search' && <IconSearch />}
                      {item.kind === 'setting' && <IconSettings />}
                    </span>
                    <span>{item.label}</span>
                    <span className="kind">{item.hint}</span>
                  </div>
                )
              })}
            </div>
          ))}

          {showSearch && (
            <div>
              <div className="palette-group-head">
                search
                {searching && <span style={{ marginLeft: 8, opacity: 0.7 }}>searching…</span>}
                {searchError && (
                  <span style={{ marginLeft: 8, color: 'var(--danger)' }}>{searchError}</span>
                )}
              </div>
              {hits.map((hit: SearchHit) => {
                runningIdx++
                const sel = runningIdx === idx
                const currentIdx = runningIdx
                return (
                  <div
                    key={hit.messageId}
                    className="palette-item"
                    data-sel={sel}
                    onMouseEnter={() => setIdx(currentIdx)}
                    onClick={() => {
                      onOpenThread(hit.threadId, hit.messageId)
                      onClose()
                    }}
                  >
                    <span className="icon">
                      <IconSearch />
                    </span>
                    <span>{hit.snippet}</span>
                    <span className="kind">{hit.threadName}</span>
                  </div>
                )
              })}
              {!searching && !searchError && hits.length === 0 && q.trim().length >= 2 && (
                <div
                  style={{
                    padding: '10px 14px',
                    fontFamily: 'var(--mono)',
                    fontSize: 11,
                    color: 'var(--muted)',
                  }}
                >
                  no corpus matches
                </div>
              )}
            </div>
          )}

          {filtered.length === 0 && !showSearch && (
            <div
              style={{
                padding: 20,
                textAlign: 'center',
                color: 'var(--muted)',
                fontFamily: 'var(--mono)',
                fontSize: 12,
              }}
            >
              No results. Press ⏎ to start a new thread with this prompt.
            </div>
          )}
        </div>
        <div className="palette-foot">
          <span><kbd>↑↓</kbd> navigate</span>
          <span><kbd>⏎</kbd> run</span>
          <span><kbd>esc</kbd> close</span>
        </div>
      </div>
    </div>
  )
}
