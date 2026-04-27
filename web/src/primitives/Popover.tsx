import { type CSSProperties, type ReactNode, type RefObject } from 'react'
import { usePopover } from './usePopover'

interface PopoverProps<Pos extends Record<string, number | undefined>> {
  open: boolean
  anchorRef: RefObject<HTMLElement | null>
  onClose: () => void
  /** Position computer — receives the anchor's DOMRect, returns the
   *  CSS offset shape the popover wants. Wrap with useCallback at the
   *  call site to keep the effect dep stable. */
  computePos: (anchor: DOMRect) => Pos
  /** Class name on the rendered <div>. Defaults to "user-menu" for
   *  consistency with the most common consumer. */
  className?: string
  /** Inline style merged on top of the computed position. */
  style?: CSSProperties
  children: ReactNode
}

/**
 * Anchored popover renderer. Pairs the usePopover lifecycle hook
 * with the rendering — caller passes `open`, anchor ref, close
 * callback, and a position computer; Popover handles the rest.
 *
 * Returns null when not open or before the position has been
 * measured. The wrapping `<div>` is `position: fixed` with the
 * computed offset spread in.
 */
export function Popover<Pos extends Record<string, number | undefined>>({
  open,
  anchorRef,
  onClose,
  computePos,
  className = 'user-menu',
  style,
  children,
}: PopoverProps<Pos>) {
  const { popRef, pos } = usePopover(open, anchorRef, onClose, computePos)
  if (!open || !pos) return null
  return (
    <div ref={popRef} className={className} style={{ position: 'fixed', ...pos, ...style }}>
      {children}
    </div>
  )
}
