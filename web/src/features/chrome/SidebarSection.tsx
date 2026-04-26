import { useState, type ReactNode } from 'react'
import { IconChevron } from '@/primitives/icons'

interface SectionProps {
  label: string
  count: number
  defaultCollapsed?: boolean
  children: ReactNode
}

export function SidebarSection({ label, count, defaultCollapsed, children }: SectionProps) {
  const [open, setOpen] = useState(!defaultCollapsed)
  return (
    <div className="sb-section" data-open={open}>
      <button className="sb-section-head" onClick={() => setOpen((v) => !v)}>
        <IconChevron size={10} />
        <span>{label}</span>
        <span className="sb-section-count">{count}</span>
      </button>
      {open && <div className="sb-section-body">{children}</div>}
    </div>
  )
}
