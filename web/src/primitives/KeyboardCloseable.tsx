import { useEffect, type ReactNode } from 'react'

interface KeyboardCloseableProps {
  /** Whether the wrapper is currently active. When false, the Escape
   *  listener is not bound. */
  open: boolean
  onClose: () => void
  /** Stop the event from bubbling to ancestor handlers. Useful when a
   *  modal is nested inside another Escape-closable scope. */
  stopPropagation?: boolean
  children: ReactNode
}

/**
 * Wraps `children` with a window-level Escape listener that fires
 * `onClose` while `open` is true. Common pattern across modals,
 * popovers, and command palette.
 *
 * The render passes children through unchanged — KeyboardCloseable is
 * effect-only, so it can wrap either a fragment or any element.
 */
export function KeyboardCloseable({
  open,
  onClose,
  stopPropagation = false,
  children,
}: KeyboardCloseableProps) {
  useEffect(() => {
    if (!open) return
    function onKey(e: KeyboardEvent) {
      if (e.key !== 'Escape') return
      if (stopPropagation) e.stopPropagation()
      onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose, stopPropagation])
  return <>{children}</>
}
