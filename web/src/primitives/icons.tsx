import type { ReactNode } from 'react'

/**
 * Icon size tokens. Scattered sizes (9, 11, 12, 13, 14) are banned
 * because they produce the visual dissonance of buttons that look
 * almost-but-not-quite aligned.
 *
 *   chrome (10): chevrons, row-action X / pin / archive, search-clears
 *                — quiet informational glyphs.
 *   button (18): every icon in an icon-only 32×32 button (topbar-btn,
 *                rail-btn, sb-icon-btn, artifacts-close) and every
 *                labeled button with a leading icon. 18 fills a 32
 *                frame at ~56%, matching the brand glyph's weight so
 *                icon-only actions read as equals of the brand mark
 *                rather than shy of it.
 */
export const ICON = {
  chrome: 10,
  button: 18,
} as const

interface IconProps {
  size?: number
  stroke?: number
}

function Icon({ path, size = ICON.button, stroke = 1.5 }: IconProps & { path: ReactNode }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth={stroke}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {path}
    </svg>
  )
}

export const IconSearch = (p: IconProps) => (
  <Icon {...p} path={<><circle cx="7" cy="7" r="4.5" /><path d="M13.5 13.5l-3-3" /></>} />
)
export const IconPlus = (p: IconProps) => (
  <Icon {...p} path={<path d="M8 3v10M3 8h10" />} />
)
export const IconChevron = (p: IconProps) => (
  <Icon {...p} path={<path d="M6 4l4 4-4 4" />} />
)
export const IconX = (p: IconProps) => (
  <Icon {...p} path={<><path d="M4 4l8 8M12 4l-8 8" /></>} />
)
export const IconArrow = (p: IconProps) => (
  <Icon {...p} path={<><path d="M3 8h10M9 4l4 4-4 4" /></>} />
)
export const IconPlay = (p: IconProps) => (
  <Icon {...p} path={<path d="M5 3l8 5-8 5z" fill="currentColor" stroke="none" />} />
)
export const IconPause = (p: IconProps) => (
  <Icon
    {...p}
    path={
      <>
        <rect x="4" y="3" width="3" height="10" fill="currentColor" stroke="none" />
        <rect x="9" y="3" width="3" height="10" fill="currentColor" stroke="none" />
      </>
    }
  />
)
export const IconStop = (p: IconProps) => (
  <Icon {...p} path={<rect x="4" y="4" width="8" height="8" fill="currentColor" stroke="none" />} />
)
export const IconLayers = (p: IconProps) => (
  <Icon {...p} path={<><path d="M8 2l6 3-6 3-6-3 6-3z" /><path d="M2 11l6 3 6-3" /></>} />
)
/** Stacked message-bubble glyph used as the rail "Chats" button —
 *  reads as "conversations" without being specific to any one thread. */
export const IconChats = (p: IconProps) => (
  <Icon
    {...p}
    path={
      <>
        <path d="M2 4.5A1.5 1.5 0 013.5 3h6A1.5 1.5 0 0111 4.5v4A1.5 1.5 0 019.5 10H6l-2.5 2V10H3.5A1.5 1.5 0 012 8.5v-4z" />
        <path d="M12.5 6h0a1.5 1.5 0 011.5 1.5v4a1.5 1.5 0 01-1.5 1.5H12v2l-2-2H7" />
      </>
    }
  />
)
export const IconBranch = (p: IconProps) => (
  <Icon
    {...p}
    path={
      <>
        <circle cx="4" cy="4" r="1.5" />
        <circle cx="4" cy="12" r="1.5" />
        <circle cx="12" cy="4" r="1.5" />
        <path d="M4 5.5v5M4 8h4a2 2 0 002-2V5.5" />
      </>
    }
  />
)
export const IconCommand = (p: IconProps) => (
  <Icon
    {...p}
    path={
      <path d="M6 10v-4M10 6v4M6 6h-2a1.5 1.5 0 110-3 1.5 1.5 0 011.5 1.5v1.5zM10 6h2a1.5 1.5 0 100-3 1.5 1.5 0 00-1.5 1.5v1.5zM6 10h-2a1.5 1.5 0 100 3 1.5 1.5 0 001.5-1.5V10zM10 10h2a1.5 1.5 0 110 3 1.5 1.5 0 01-1.5-1.5V10z" />
    }
  />
)
export const IconSettings = (p: IconProps) => (
  <Icon
    {...p}
    path={
      <>
        <circle cx="8" cy="8" r="2" />
        <path d="M8 1v2M8 13v2M1 8h2M13 8h2M3 3l1.5 1.5M11.5 11.5L13 13M3 13l1.5-1.5M11.5 4.5L13 3" />
      </>
    }
  />
)
export const IconSkill = (p: IconProps) => (
  <Icon
    {...p}
    path={<path d="M8 1l2 4.5 5 .5-3.75 3.4L12.5 15 8 12.5 3.5 15l1.25-5.6L1 6l5-.5z" />}
  />
)
export const IconSun = (p: IconProps) => (
  <Icon
    {...p}
    path={
      <>
        <circle cx="8" cy="8" r="2.5" />
        <path d="M8 1.5v1.5M8 13v1.5M1.5 8h1.5M13 8h1.5M3 3l1.1 1.1M11.9 11.9L13 13M3 13l1.1-1.1M11.9 4.1L13 3" />
      </>
    }
  />
)
export const IconMoon = (p: IconProps) => (
  <Icon {...p} path={<path d="M13.5 9.5A6 6 0 017 3a6 6 0 106.5 6.5z" />} />
)
export const IconPin = (p: IconProps) => (
  <Icon {...p} path={<path d="M10 2l4 4-3 1-2 2-1 4-4-4 4-1 2-2z" />} />
)
export const IconArchive = (p: IconProps) => (
  <Icon {...p} path={<><path d="M2 4h12v3H2zM3 7v7h10V7" /><path d="M6 10h4" /></>} />
)
export const IconEdit = (p: IconProps) => (
  <Icon
    {...p}
    path={
      <>
        <path d="M11.5 2.5l2 2-8 8H3.5v-2z" />
        <path d="M10 4l2 2" />
      </>
    }
  />
)
export const IconTrash = (p: IconProps) => (
  <Icon
    {...p}
    path={
      <>
        <path d="M3 4h10M6 4V3a1 1 0 011-1h2a1 1 0 011 1v1M4.5 4l.5 9a1 1 0 001 1h4a1 1 0 001-1l.5-9" />
        <path d="M7 7v5M9 7v5" />
      </>
    }
  />
)
export const IconPanel = (p: IconProps) => (
  <Icon {...p} path={<><rect x="2" y="3" width="12" height="10" rx="1" /><path d="M6 3v10" /></>} />
)
/** IconPanel's mirror — divider on the right. For the artifacts
 *  toggle, which sits on the right of the screen and expands a panel
 *  in that direction. */
export const IconPanelRight = (p: IconProps) => (
  <Icon {...p} path={<><rect x="2" y="3" width="12" height="10" rx="1" /><path d="M10 3v10" /></>} />
)
export const IconPaperclip = (p: IconProps) => (
  <Icon {...p} path={<path d="M11 5.5L6 10.5a2 2 0 102.8 2.8l5-5a3.5 3.5 0 00-5-5l-5.5 5.5a5 5 0 007 7l5-5" />} />
)
export const IconSpinner = (p: IconProps) => (
  <Icon
    {...p}
    path={
      <path d="M8 2a6 6 0 016 6" opacity="1" />
    }
  />
)

// Spidey mark — a geometric web. Six nodes around a center, one accent node,
// lightweight threads. Sized by the size prop; uses currentColor for the
// threads and accent prop for the highlighted node.
export function Spidey({ size = 18, accent = 'var(--accent)' }: { size?: number; accent?: string }) {
  const r = 7
  const cx = 10,
    cy = 10
  const pts = Array.from({ length: 6 }, (_, i) => {
    const a = (Math.PI * 2 * i) / 6 - Math.PI / 2
    return [cx + r * Math.cos(a), cy + r * Math.sin(a)] as const
  })
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 20 20"
      aria-hidden="true"
      style={{ display: 'block' }}
    >
      {pts.map(([x, y], i) => (
        <line key={'s' + i} x1={cx} y1={cy} x2={x} y2={y} stroke="currentColor" strokeWidth="0.7" opacity="0.45" />
      ))}
      {pts.map(([x, y], i) => {
        const [x2, y2] = pts[(i + 1) % 6]
        return <line key={'e' + i} x1={x} y1={y} x2={x2} y2={y2} stroke="currentColor" strokeWidth="0.7" opacity="0.45" />
      })}
      {pts.map(([x, y], i) => (
        <circle key={'n' + i} cx={x} cy={y} r="1.1" fill={i === 0 ? accent : 'currentColor'} />
      ))}
      <circle cx={cx} cy={cy} r="1.4" fill="currentColor" />
    </svg>
  )
}
