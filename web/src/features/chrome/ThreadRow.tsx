import { useRef, useState } from 'react'
import { IconArchive, IconChevron, IconX } from '@/primitives/icons'
import { isAutonomous } from '@/domain/derive'
import type { ThreadSummary } from '@/hooks/useThreads'

interface ThreadRowProps {
  thread: ThreadSummary
  active: boolean
  onClick: () => void
  child?: boolean
  hasBranches?: boolean
  showBranches?: boolean
  onToggleBranches?: () => void
  onArchiveToggle?: () => void
  onDelete?: () => void
  onRename?: (newName: string) => void
}

export function ThreadRow({
  thread,
  active,
  onClick,
  child,
  hasBranches,
  showBranches,
  onToggleBranches,
  onArchiveToggle,
  onDelete,
  onRename,
}: ThreadRowProps) {
  const autonomous = isAutonomous(thread.mode)
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(thread.name)
  // Escape sets this so a blur-on-unmount triggered by setEditing(false)
  // doesn't commit the half-typed text as a rename. Reset on every new
  // edit session.
  const canceledRef = useRef(false)

  function commit() {
    setEditing(false)
    if (canceledRef.current) {
      canceledRef.current = false
      return
    }
    const name = draft.trim()
    if (!name || name === thread.name) return
    onRename?.(name)
  }

  return (
    <div
      className="thread-row"
      data-active={active}
      data-child={child ? 'true' : undefined}
      data-autonomous={autonomous ? 'true' : undefined}
      onClick={editing ? undefined : onClick}
      onDoubleClick={(e) => {
        if (!onRename) return
        e.stopPropagation()
        setDraft(thread.name)
        canceledRef.current = false
        setEditing(true)
      }}
    >
      {hasBranches ? (
        <button
          className="thread-branch-toggle"
          onClick={(e) => {
            e.stopPropagation()
            onToggleBranches?.()
          }}
          data-open={showBranches}
          title={showBranches ? 'Hide branches' : 'Show branches'}
        >
          <IconChevron size={10} />
        </button>
      ) : (
        <span className="thread-branch-slot" />
      )}
      {editing ? (
        <input
          autoFocus
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onClick={(e) => e.stopPropagation()}
          onBlur={commit}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              commit()
            } else if (e.key === 'Escape') {
              canceledRef.current = true
              setEditing(false)
              setDraft(thread.name)
            }
          }}
          style={{
            flex: 1,
            minWidth: 0,
            padding: '1px 4px',
            fontSize: 13,
            fontFamily: 'var(--sans)',
            background: 'var(--paper)',
            border: '1px solid var(--rule)',
            borderRadius: 3,
            color: 'var(--ink)',
          }}
        />
      ) : (
        <span className="thread-row-name">{thread.name}</span>
      )}
      {!editing && autonomous && <span className="thread-row-tick" title="autonomous run" />}
      {!editing && onArchiveToggle && (
        <button
          className="thread-row-action"
          onClick={(e) => {
            e.stopPropagation()
            onArchiveToggle()
          }}
          title={thread.archivedAt ? 'unarchive' : 'archive'}
        >
          <IconArchive size={10} />
        </button>
      )}
      {!editing && onDelete && (
        <button
          className="thread-row-action"
          onClick={(e) => {
            e.stopPropagation()
            if (window.confirm(`Delete thread "${thread.name}"? This can't be undone.`)) {
              onDelete()
            }
          }}
          title="delete"
          style={{ color: 'var(--danger)' }}
        >
          <IconX size={10} />
        </button>
      )}
    </div>
  )
}
