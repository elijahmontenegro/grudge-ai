import { useEffect, useRef, type RefObject } from 'react'

export interface ScrollbarIndicator {
  /** 0..1 fractional position down the scroll range. */
  frac: number
  kind: 'user' | 'error' | 'question' | 'plan' | 'selected'
  title?: string
}

interface StreamScrollbarProps {
  scrollRef: RefObject<HTMLDivElement | null>
  indicators?: ScrollbarIndicator[]
}

/**
 * Custom scrollbar for the thread stream. Lives in its own grid column
 * between the chat and the minimap (VS Code's `content | scrollbar |
 * minimap` order). Indicators ride on an overview-ruler lane on the
 * track itself — errors, awaiting-answer questions, pending plan
 * approvals.
 *
 * Performance: thumb position is mutated directly on the DOM inside a
 * scroll listener (no React state, no re-render on every scroll tick).
 * Drag updates `stream.scrollTop` directly too — the scroll event then
 * updates the thumb position, closing the loop without touching React.
 * This is what the old CorpusRail did poorly: it went through setState
 * on every pointermove, which choked large corpora. Native stream
 * scrolling (wheel, keyboard, PgUp/Dn) works because it all flows
 * through the same scrollTop setter.
 */
export function StreamScrollbar({ scrollRef, indicators = [] }: StreamScrollbarProps) {
  const trackRef = useRef<HTMLDivElement>(null)
  const thumbRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const el = scrollRef.current
    const track = trackRef.current
    const thumb = thumbRef.current
    if (!el || !track || !thumb) return
    let rafId = 0
    // Pixel-space math. Using percentages + a min-height in CSS lets
    // the thumb bottom drift past the track bottom when the minimum
    // kicks in; computing top/height in pixels anchored to the track's
    // measured height guarantees the thumb always fits inside the
    // inset track area and honours the 4px top/bottom padding.
    const minThumbPx = 40
    function update() {
      if (!el || !track || !thumb) return
      const full = el.scrollHeight || 1
      const view = el.clientHeight || 0
      const trackH = track.clientHeight || 0
      if (trackH <= 0) return
      const maxScroll = Math.max(1, full - view)
      const rawThumbH = (view / full) * trackH
      const thumbH = Math.max(minThumbPx, Math.min(trackH, rawThumbH))
      const topPx = (el.scrollTop / maxScroll) * (trackH - thumbH)
      thumb.style.top = `${topPx}px`
      thumb.style.height = `${thumbH}px`
    }
    function scheduleUpdate() {
      if (rafId) return
      rafId = requestAnimationFrame(() => {
        rafId = 0
        update()
      })
    }
    update()
    el.addEventListener('scroll', scheduleUpdate, { passive: true })
    const ro = new ResizeObserver(update)
    ro.observe(el)
    ro.observe(track)
    return () => {
      el.removeEventListener('scroll', scheduleUpdate)
      ro.disconnect()
      if (rafId) cancelAnimationFrame(rafId)
    }
  }, [scrollRef])

  function onTrackPointerDown(e: React.PointerEvent<HTMLDivElement>) {
    if (e.button !== 0) return
    const track = trackRef.current
    const el = scrollRef.current
    if (!track || !el) return
    e.preventDefault()
    track.setPointerCapture(e.pointerId)
    const prevBehavior = el.style.scrollBehavior
    el.style.scrollBehavior = 'auto'

    function scrollFromY(clientY: number) {
      if (!track || !el) return
      const rect = track.getBoundingClientRect()
      const frac = Math.max(0, Math.min(1, (clientY - rect.top) / rect.height))
      const target = frac * el.scrollHeight - el.clientHeight / 2
      el.scrollTop = Math.max(0, Math.min(el.scrollHeight - el.clientHeight, target))
    }
    scrollFromY(e.clientY)

    function onMove(ev: PointerEvent) {
      scrollFromY(ev.clientY)
    }
    function onUp() {
      if (track) track.releasePointerCapture?.(e.pointerId)
      if (el) el.style.scrollBehavior = prevBehavior || ''
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }

  function onWheel(e: React.WheelEvent<HTMLDivElement>) {
    const el = scrollRef.current
    if (!el) return
    el.scrollTop += e.deltaY
  }

  return (
    <div className="stream-scrollbar" onWheel={onWheel}>
      <div
        ref={trackRef}
        className="stream-scrollbar-track"
        onPointerDown={onTrackPointerDown}
      >
        {indicators.map((ind, i) => (
          <div
            key={i}
            className="stream-scrollbar-indicator"
            data-kind={ind.kind}
            style={{ top: `${ind.frac * 100}%` }}
            title={ind.title}
          />
        ))}
        <div ref={thumbRef} className="stream-scrollbar-thumb" />
      </div>
    </div>
  )
}
