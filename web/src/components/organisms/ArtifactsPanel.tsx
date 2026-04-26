import { useEffect, useMemo, useRef, useState } from 'react'
import { IconChevron, IconPanelRight } from '@/components/atoms/icons'
import { AsciiDocBody } from '@/components/atoms/AsciiDocBody'
import { aggregateArtifacts } from '@/data/artifacts'
import { useLocalStorage } from '@/hooks/useLocalStorage'
import type { Artifact, ArtifactOp, Message } from '@/data/types'

const WIDTH_STORAGE_KEY = 'spidey.artifactsWidth'
const DEFAULT_WIDTH = 720
const MIN_WIDTH = 320
const MAX_WIDTH = 1200

const widthCodec = {
  serialize: (n: number) => String(n),
  parse: (raw: string) => {
    const n = parseInt(raw, 10)
    if (!Number.isFinite(n)) return DEFAULT_WIDTH
    return Math.max(MIN_WIDTH, Math.min(MAX_WIDTH, n))
  },
}

interface ArtifactsPanelProps {
  corpus: Message[]
  /** Latest compiled plan content from agent.planContent. Used only as a
   *  fallback for the plan body when the FileWrite hasn't refetched yet. */
  livePlanContent?: string | null
  /** Collapsed: panel renders null (topbar owns the opener).
   *  Expanded: panel renders its own close button in the header — the
   *  two buttons share `.sb-icon-btn` styling so the control reads as
   *  "one button that never moves" even though it's two different
   *  elements. Neither carries an active/on state; it's just a
   *  toggle, same as the sidebar's collapse button. */
  collapsed: boolean
  onClose: () => void
}

/**
 * Artifact inspector — pure viewer. Every file the model touched via
 * structured tool calls shows up here. Plan.adoc renders as AsciiDoc;
 * other files show body or diff in a <pre>. Actions (approve / reject /
 * edit) live in the chat flow (PlanApprovalCard), not here.
 */
export function ArtifactsPanel({
  corpus,
  livePlanContent,
  collapsed,
  onClose,
}: ArtifactsPanelProps) {
  const artifacts = useMemo(() => aggregateArtifacts(corpus), [corpus])
  const [width, setWidth] = useLocalStorage<number>(
    WIDTH_STORAGE_KEY,
    DEFAULT_WIDTH,
    widthCodec,
  )
  const [resizing, setResizing] = useState(false)
  const panelRef = useRef<HTMLElement>(null)

  function onHandlePointerDown(e: React.PointerEvent<HTMLDivElement>) {
    e.preventDefault()
    const startX = e.clientX
    const startWidth = width
    setResizing(true)
    const handle = e.currentTarget
    handle.setPointerCapture(e.pointerId)
    function onMove(ev: PointerEvent) {
      const dx = startX - ev.clientX
      const next = Math.max(MIN_WIDTH, Math.min(MAX_WIDTH, startWidth + dx))
      setWidth(next)
    }
    function onUp() {
      handle.releasePointerCapture?.(e.pointerId)
      setResizing(false)
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }

  // When collapsed, don't render anything — the topbar owns the toggle
  // button; there's no hover-strip or chrome to represent the panel
  // when it's hidden. Keeps the UI to a single-entry toggle with no
  // duplicated affordance.
  if (collapsed) return null

  return (
    <aside
      ref={panelRef}
      className="artifacts-panel expanded"
      data-resizing={resizing || undefined}
      style={{ width: `${width}px` }}
    >
      <div
        className="artifacts-resize-handle"
        onPointerDown={onHandlePointerDown}
        onDoubleClick={() => setWidth(DEFAULT_WIDTH)}
        title="Drag to resize · double-click to reset"
        aria-label="Resize artifacts panel"
      />
      <header className="artifacts-head">
        <span className="artifacts-title">artifacts</span>
        <span className="artifacts-count">{artifacts.length}</span>
        <span className="artifacts-head-spacer" />
        {/* Uses `.sb-icon-btn` (same class as the sidebar's collapse
            toggle and the topbar's artifacts opener) so the close
            button is visually indistinguishable from its collapsed-
            state counterpart — same frame, same icon, same hover,
            no "active" tint. It's just a toggle, not a pressed
            state. */}
        <button
          className="sb-icon-btn"
          onClick={onClose}
          title="Hide artifacts"
          aria-label="Hide artifacts"
        >
          <IconPanelRight size={18} />
        </button>
      </header>
      <div className="artifacts-body scroll">
        {artifacts.length === 0 && (
          <div className="artifacts-empty">
            No files touched yet. Tool calls like FileRead, FileWrite, FileEdit will appear here.
          </div>
        )}
        {artifacts.map((a) => (
          <ArtifactEntry
            key={a.path}
            artifact={a}
            livePlanContent={a.isPlan ? livePlanContent ?? undefined : undefined}
          />
        ))}
      </div>
    </aside>
  )
}

interface ArtifactEntryProps {
  artifact: Artifact
  livePlanContent?: string
}

function opLabel(op: ArtifactOp): string {
  return op === 'read' ? 'read' : op === 'write' ? 'wrote' : op === 'edit' ? 'edited' : 'deleted'
}

// Collapse a path down to its last two segments so the listing shows
// enough context to disambiguate same-named files in different dirs
// (e.g. `research/biological-framework.md` vs `nekomimi/OUTLINE.md`)
// without pushing the whole absolute path into the row. Full path is
// still in the title attribute on hover.
function displayPath(path: string): string {
  const parts = path.split(/[\\/]/).filter(Boolean)
  if (parts.length <= 1) return parts[0] || path
  return parts.slice(-2).join('/')
}

// The aggregator records per-touch isError (matched on tool-result
// shape — "error", "failed", etc.). An artifact is "latest-errored"
// if its most recent touch is flagged. Used to tint the row so the
// user can skim-spot the failed writes amid the successful ones.
function latestTouchIsError(artifact: Artifact): boolean {
  const latest = artifact.touches[artifact.touches.length - 1]
  return !!latest?.isError
}

function ArtifactEntry({ artifact, livePlanContent }: ArtifactEntryProps) {
  const [open, setOpen] = useState(artifact.isPlan)
  useEffect(() => {
    if (artifact.isPlan && livePlanContent) setOpen(true)
  }, [artifact.isPlan, livePlanContent])

  const errored = latestTouchIsError(artifact)

  return (
    <div
      className="artifact-entry"
      data-plan={artifact.isPlan || undefined}
      data-op={artifact.latestOp}
      data-error={errored || undefined}
    >
      <button className="artifact-head" onClick={() => setOpen((v) => !v)}>
        <span className="artifact-chev" data-open={open}>
          <IconChevron size={10} />
        </span>
        <span className="artifact-op-badge">{opLabel(artifact.latestOp)}</span>
        <span className="artifact-path" title={artifact.path}>
          {displayPath(artifact.path)}
        </span>
        {artifact.touches.length > 1 && (
          <span className="artifact-touch-count">{artifact.touches.length}</span>
        )}
      </button>
      {open && (
        <div className="artifact-body">
          {artifact.isPlan ? (
            <PlanEntryBody artifact={artifact} livePlanContent={livePlanContent} />
          ) : (
            <GenericEntryBody artifact={artifact} />
          )}
        </div>
      )}
    </div>
  )
}

function GenericEntryBody({ artifact }: { artifact: Artifact }) {
  const latest = artifact.touches[artifact.touches.length - 1]
  if (!latest) return null
  if (latest.op === 'edit' && latest.edit) {
    return (
      <div className="artifact-edit">
        <div className="artifact-edit-label">removed</div>
        <pre className="artifact-pre removed">{latest.edit.oldString || '(empty)'}</pre>
        <div className="artifact-edit-label">added</div>
        <pre className="artifact-pre added">{latest.edit.newString || '(empty)'}</pre>
      </div>
    )
  }
  const body = latest.body ?? '(no content captured)'
  return <pre className="artifact-pre">{body}</pre>
}

function PlanEntryBody({
  artifact,
  livePlanContent,
}: {
  artifact: Artifact
  livePlanContent?: string
}) {
  const latestWrite = [...artifact.touches].reverse().find((t) => t.op === 'write')
  const source = livePlanContent || latestWrite?.body || '(plan not captured yet — retry)'
  return (
    <div className="artifact-plan-preview">
      <AsciiDocBody source={source} />
    </div>
  )
}
