import { useCallback, type RefObject } from 'react'
import { IconArchive, IconEdit, IconTrash } from '@/primitives/icons'
import type { Thread } from '@/domain/types'
import { usePopover } from '@/primitives/usePopover'

interface Props {
  anchorRef: RefObject<HTMLElement | null>
  thread: Thread & { archived?: boolean }
  onClose: () => void
  onRename: () => void
  onToggleArchive: () => void
  onDelete: () => void
}

/**
 * Popover anchored below the topbar's thread-name button. Mirrors
 * the claude.ai thread-actions dropdown: Rename / Archive / Delete
 * grouped in a small menu. Star isn't here yet — the Thread schema
 * has no `pinned` mutation on the backend, so shipping it now would
 * be a dead button. Add it when the backend slice lands.
 */
export function ThreadActionsMenu({
  anchorRef,
  thread,
  onClose,
  onRename,
  onToggleArchive,
  onDelete,
}: Props) {
  const computePos = useCallback(
    (r: DOMRect) => ({ left: r.left, top: r.bottom + 4 }),
    [],
  )
  const { popRef, pos } = usePopover(true, anchorRef, onClose, computePos)

  if (!pos) return null
  return (
    <div
      className="thread-menu"
      ref={popRef}
      style={{ position: 'fixed', left: pos.left, top: pos.top }}
      role="menu"
    >
      <button className="thread-menu-item" onClick={onRename} role="menuitem">
        <span className="thread-menu-icon"><IconEdit size={13} /></span>
        <span>Rename</span>
      </button>
      <button className="thread-menu-item" onClick={onToggleArchive} role="menuitem">
        <span className="thread-menu-icon"><IconArchive size={13} /></span>
        <span>{thread.archived ? 'Unarchive' : 'Archive'}</span>
      </button>
      <button className="thread-menu-item danger" onClick={onDelete} role="menuitem">
        <span className="thread-menu-icon"><IconTrash size={13} /></span>
        <span>Delete</span>
      </button>
    </div>
  )
}
