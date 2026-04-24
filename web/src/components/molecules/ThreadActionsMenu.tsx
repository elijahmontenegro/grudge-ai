import { useEffect, useRef, useState, type RefObject } from 'react'
import { IconArchive, IconEdit, IconTrash } from '@/components/atoms/icons'
import type { Thread } from '@/data/types'

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
  const ref = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null)

  useEffect(() => {
    function place() {
      const a = anchorRef.current
      if (!a) return
      const r = a.getBoundingClientRect()
      setPos({ left: r.left, top: r.bottom + 4 })
    }
    place()
    function onDoc(e: MouseEvent) {
      if (
        ref.current &&
        !ref.current.contains(e.target as Node) &&
        !anchorRef.current?.contains(e.target as Node)
      ) {
        onClose()
      }
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('resize', place)
    window.addEventListener('scroll', place, true)
    document.addEventListener('mousedown', onDoc)
    document.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('resize', place)
      window.removeEventListener('scroll', place, true)
      document.removeEventListener('mousedown', onDoc)
      document.removeEventListener('keydown', onKey)
    }
  }, [anchorRef, onClose])

  if (!pos) return null
  return (
    <div
      className="thread-menu"
      ref={ref}
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
