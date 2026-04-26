import { useCallback, useEffect, useRef, useState, type RefObject } from 'react'

/**
 * Anchored popover lifecycle: position from a ref, close on outside
 * mousedown, close on Escape, recompute position on resize/scroll.
 *
 * Three popover components (UserMenu, ThreadActionsMenu,
 * ThreadConfigPopover) hand-rolled this same skeleton. The
 * positioning math is the only thing that differs — the doc-click
 * gate and the listener wiring are mechanical.
 *
 * The caller supplies `computePos`, which receives the anchor's
 * DOMRect and returns whatever position shape the popover's CSS
 * needs (Tailwind absolute, fixed top/left/right/bottom, etc.).
 * Wrap it in `useCallback` at the call site so the effect doesn't
 * re-bind every render.
 */
export function usePopover<Pos>(
  open: boolean,
  anchorRef: RefObject<HTMLElement | null>,
  onClose: () => void,
  computePos: (anchor: DOMRect) => Pos,
): { popRef: RefObject<HTMLDivElement | null>; pos: Pos | null } {
  const popRef = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<Pos | null>(null)

  useEffect(() => {
    if (!open) {
      setPos(null)
      return
    }
    function place() {
      const a = anchorRef.current
      if (!a) return
      setPos(computePos(a.getBoundingClientRect()))
    }
    place()
    function onDoc(e: MouseEvent) {
      if (
        popRef.current &&
        !popRef.current.contains(e.target as Node) &&
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
  }, [open, anchorRef, onClose, computePos])

  return { popRef, pos }
}

// Re-export for caller convenience: most positioning closures are
// stable, but using useCallback inline keeps the hook signature
// honest about its dep on computePos.
export { useCallback }
