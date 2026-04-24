import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router'
import { ThreadConfigPopover } from '@/components/organisms/ThreadConfigPopover'
import { IconChevron, IconPanelRight } from '@/components/atoms/icons'
import { ThreadActionsMenu } from '@/components/molecules/ThreadActionsMenu'
import { useThreadMutations } from '@/hooks/useThreadMutations'
import type { Thread } from '@/data/types'

interface TopbarProps {
  view: 'home' | 'thread' | 'settings' | 'firstrun' | string
  thread?: Thread & { workingDirs?: string[]; sandboxed?: boolean }
  /** Artifacts panel collapse state. Undefined when there's no panel
   *  to toggle (non-thread routes). */
  artifactsCollapsed?: boolean
  onToggleArtifacts?: () => void
}

/**
 * Breadcrumb + right-side chrome. On thread routes the thread name is
 * a live element: click it → actions menu (rename / archive / delete),
 * double-click (or the Rename menu item) → inline edit. The topbar is
 * scoped to the chat area only — the artifacts panel occupies its own
 * full-height column to the right.
 */
export function Topbar({
  view,
  thread,
  artifactsCollapsed,
  onToggleArtifacts,
}: TopbarProps) {
  const isThreadView = view === 'thread' && thread
  const [menuOpen, setMenuOpen] = useState(false)
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const menuBtnRef = useRef<HTMLButtonElement>(null)
  const canceledRef = useRef(false)
  const muts = useThreadMutations()
  const navigate = useNavigate()

  // Close the menu whenever we navigate to a different thread — otherwise
  // an open menu would silently apply its next click to the wrong thread.
  useEffect(() => {
    setMenuOpen(false)
    setEditing(false)
  }, [thread?.id])

  function startRename() {
    if (!thread) return
    setDraft(thread.name)
    canceledRef.current = false
    setEditing(true)
    setMenuOpen(false)
  }
  function commitRename() {
    setEditing(false)
    if (canceledRef.current || !thread) {
      canceledRef.current = false
      return
    }
    const name = draft.trim()
    if (!name || name === thread.name) return
    void muts.rename(thread.id, name)
  }
  function toggleArchive() {
    if (!thread) return
    if (thread.archived) void muts.unarchive(thread.id)
    else void muts.archive(thread.id)
    setMenuOpen(false)
  }
  function remove() {
    if (!thread) return
    setMenuOpen(false)
    if (!window.confirm(`Delete thread "${thread.name}"? This can't be undone.`)) return
    void muts.remove(thread.id).then(() => navigate('/'))
  }

  return (
    <div className="topbar">
      <div className="crumbs">
        {isThreadView && thread && (
          <>
            <span>/</span>
            {/* Two buttons, two responsibilities: click the name →
                inline rename. Click the chevron → actions menu
                anchored to the chevron's own rect. The chevron
                always renders (even mid-rename) so the menu stays
                reachable and doesn't visually jump as the name
                swaps for the input. */}
            {editing ? (
              <input
                className="crumb-rename"
                autoFocus
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                onBlur={commitRename}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault()
                    commitRename()
                  } else if (e.key === 'Escape') {
                    canceledRef.current = true
                    setEditing(false)
                    setDraft(thread.name)
                  }
                }}
                /* Width tracks the draft length via `ch` units so the
                   field stays the same visual size as the name it
                   replaced — no jarring jump to a fixed min-width.
                   Math.max(4) keeps a clickable minimum when the
                   field is empty mid-typing. */
                style={{ width: `${Math.max(draft.length, 4) + 2}ch` }}
              />
            ) : (
              <button
                className="crumb-name"
                onClick={startRename}
                title="Rename thread"
              >
                <b>{thread.name}</b>
              </button>
            )}
            <button
              ref={menuBtnRef}
              className="crumb-chevron"
              data-open={menuOpen || undefined}
              onClick={() => setMenuOpen((v) => !v)}
              title="Thread actions"
              aria-label="Thread actions"
            >
              <IconChevron size={10} />
            </button>
          </>
        )}
        {view === 'home' && (
          <>
            <span>/</span>
            <b>home</b>
          </>
        )}
        {view === 'chats' && (
          <>
            <span>/</span>
            <b>threads</b>
          </>
        )}
        {view === 'settings' && (
          <>
            <span>/</span>
            <b>settings</b>
          </>
        )}
      </div>
      {isThreadView && thread && menuOpen && (
        <ThreadActionsMenu
          anchorRef={menuBtnRef}
          thread={thread}
          onClose={() => setMenuOpen(false)}
          onRename={startRename}
          onToggleArchive={toggleArchive}
          onDelete={remove}
        />
      )}
      <span className="spacer" />
      {isThreadView && thread && (
        <ThreadConfigPopover
          threadId={thread.id}
          workingDirs={thread.workingDirs ?? []}
          sandboxed={thread.sandboxed ?? false}
        />
      )}
      {/* Collapsed: this button opens the panel. Expanded: the panel
          renders its own open/close button in its header using the
          same `.sb-icon-btn` styling, at the same y-coordinate. The
          visible control reads as a single button that never moves —
          no active/on state, just a neutral toggle (matches the
          sidebar's collapse button treatment). */}
      {isThreadView && onToggleArtifacts && artifactsCollapsed && (
        <button
          className="sb-icon-btn"
          onClick={onToggleArtifacts}
          title="Show artifacts"
          aria-label="Show artifacts"
        >
          <IconPanelRight size={18} />
        </button>
      )}
    </div>
  )
}
