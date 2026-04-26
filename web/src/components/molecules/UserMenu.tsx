import { useCallback, type RefObject } from 'react'
import { IconSun, IconMoon, IconSettings, IconLayers } from '@/components/atoms/icons'
import { useMe } from '@/hooks/useMe'
import { usePopover } from '@/hooks/usePopover'

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
  const me = useMe()

  const computePos = useCallback(
    (r: DOMRect): Pos => {
      if (placement === 'right') {
        return { left: r.right + 8, bottom: Math.max(8, window.innerHeight - r.bottom) }
      }
      const width = Math.max(r.width, 240)
      return { left: r.left, width, bottom: window.innerHeight - r.top + 6 }
    },
    [placement],
  )
  const { popRef, pos } = usePopover(open, anchorRef, onClose, computePos)

  if (!open || !pos) return null
  const style: React.CSSProperties = { position: 'fixed', ...pos }
  return (
    <div className="user-menu" ref={popRef} style={style}>
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
