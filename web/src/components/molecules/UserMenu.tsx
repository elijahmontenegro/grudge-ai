import { useEffect, useRef, useState, type RefObject } from 'react'
import { IconSun, IconMoon, IconSettings, IconLayers } from '@/components/atoms/icons'
import { useMe } from '@/hooks/useMe'

interface UserMenuProps {
  open: boolean
  anchorRef: RefObject<HTMLElement | null>
  placement?: 'top' | 'right'
  onClose: () => void
  theme: 'light' | 'dark'
  onToggleTheme: () => void
  onOpenFirstRun: () => void
  onOpenSettings: () => void
}

interface Pos {
  left: number
  bottom: number
  width?: number
}

export function UserMenu({
  open,
  anchorRef,
  placement = 'top',
  onClose,
  theme,
  onToggleTheme,
  onOpenFirstRun,
  onOpenSettings,
}: UserMenuProps) {
  const ref = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<Pos | null>(null)
  const me = useMe()

  useEffect(() => {
    if (!open) return
    function place() {
      const a = anchorRef.current
      if (!a) return
      const r = a.getBoundingClientRect()
      if (placement === 'right') {
        setPos({ left: r.right + 8, bottom: Math.max(8, window.innerHeight - r.bottom) })
      } else {
        const width = Math.max(r.width, 240)
        setPos({ left: r.left, width, bottom: window.innerHeight - r.top + 6 })
      }
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
  }, [open, anchorRef, placement, onClose])

  if (!open || !pos) return null
  const style: React.CSSProperties = { position: 'fixed', ...pos }
  return (
    <div className="user-menu" ref={ref} style={style}>
      <div className="user-menu-head">
        <div className="user-menu-name">{me.name}</div>
        <div className="user-menu-sub">{me.host}</div>
      </div>
      <div className="user-menu-group">
        <button className="user-menu-item" onClick={onToggleTheme}>
          <span className="user-menu-icon">{theme === 'dark' ? <IconSun size={13} /> : <IconMoon size={13} />}</span>
          <span>{theme === 'dark' ? 'Light appearance' : 'Dark appearance'}</span>
        </button>
        <button className="user-menu-item" onClick={onOpenSettings}>
          <span className="user-menu-icon"><IconSettings size={13} /></span>
          <span>Settings</span>
        </button>
        <button className="user-menu-item" onClick={onOpenFirstRun}>
          <span className="user-menu-icon"><IconLayers size={13} /></span>
          <span>Model providers</span>
        </button>
      </div>
    </div>
  )
}
