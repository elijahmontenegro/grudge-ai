import type { ReactNode } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import { BranchNavigator } from '@/components/molecules/BranchNavigator'
import { useAgentState } from '@/hooks/useAgentState'

interface ThreadLayoutProps {
  threadId: string
  threadName: string
  messageCount: number
  children: ReactNode
  introspectionPanel?: ReactNode
  showIntrospection: boolean
  onToggleIntrospection: () => void
  footer: ReactNode
}

export function ThreadLayout({
  threadId,
  threadName,
  messageCount,
  children,
  introspectionPanel,
  showIntrospection,
  onToggleIntrospection,
  footer,
}: ThreadLayoutProps) {
  const { state: agentState } = useAgentState(threadId)

  return (
    <main className="flex-1 flex flex-col min-w-0">
      <header className="border-b border-border p-3 flex items-center gap-3">
        <h1 className="text-sm font-semibold flex-1 truncate">{threadName}</h1>
        <BranchNavigator currentThreadId={threadId} />
        <Badge variant="outline" className="text-xs">{messageCount} msgs</Badge>
        {agentState && (
          <Badge
            variant={agentState.status === 'RUNNING' ? 'default' : 'secondary'}
            className="text-xs"
          >
            {agentState.mode !== 'NORMAL' ? agentState.mode : agentState.status}
          </Badge>
        )}
        <Button
          variant="ghost"
          size="sm"
          className="text-xs"
          onClick={onToggleIntrospection}
        >
          {showIntrospection ? 'Hide RRC' : 'RRC'}
        </Button>
      </header>

      <div className="flex flex-1 min-h-0">
        <div className="flex-1 flex flex-col min-w-0">
          {children}
          <Separator />
          {footer}
        </div>
        {showIntrospection && introspectionPanel}
      </div>
    </main>
  )
}
