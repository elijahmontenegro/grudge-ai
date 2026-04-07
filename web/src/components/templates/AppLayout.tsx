import type { ReactNode } from 'react'
import { ThreadSidebar } from '@/components/organisms/ThreadSidebar'
import { CommandPalette } from '@/components/organisms/CommandPalette'

interface AppLayoutProps {
  children: ReactNode
  showSidebar?: boolean
}

export function AppLayout({ children, showSidebar = true }: AppLayoutProps) {
  return (
    <>
      <CommandPalette />
      <div className="flex h-screen">
        {showSidebar && <ThreadSidebar />}
        {children}
      </div>
    </>
  )
}
