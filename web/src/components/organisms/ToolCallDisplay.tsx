import { gql } from '@apollo/client'
import { useMutation } from '@apollo/client/react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

const APPROVE_TOOL = gql`
  mutation ApproveToolCall($callId: ID!) {
    approveToolCall(callId: $callId)
  }
`

const DENY_TOOL = gql`
  mutation DenyToolCall($callId: ID!, $reason: String) {
    denyToolCall(callId: $callId, reason: $reason)
  }
`

interface ToolCallDisplayProps {
  callId: string
  toolName: string
  arguments: string
  status: string
  result: string | null
  isError: boolean | null
}

export function ToolCallDisplay({
  callId,
  toolName,
  arguments: args,
  status,
  result,
  isError,
}: ToolCallDisplayProps) {
  const [approve] = useMutation<{approveToolCall: boolean}>(APPROVE_TOOL)
  const [deny] = useMutation<{denyToolCall: boolean}>(DENY_TOOL)

  return (
    <div className={cn(
      'rounded-xl p-4 text-sm border',
      isError ? 'border-destructive/20 bg-destructive/5' : 'border-border bg-card',
    )}>
      <div className="flex items-center gap-2 mb-2">
        <span className="text-xs font-medium">{toolName}</span>
        <span className={cn(
          'text-[10px] px-1.5 py-0.5 rounded-full',
          status === 'pending' ? 'bg-amber-500/10 text-amber-500' :
          status === 'running' ? 'bg-primary/10 text-primary animate-pulse-subtle' :
          'bg-secondary text-muted-foreground'
        )}>
          {status}
        </span>
      </div>

      <pre className="text-xs text-muted-foreground bg-secondary/50 p-2.5 rounded-lg overflow-auto max-h-32 font-mono">
        {args}
      </pre>

      {result && (
        <pre className={cn(
          'text-xs p-2.5 rounded-lg overflow-auto max-h-48 mt-2 font-mono',
          isError ? 'bg-destructive/5 text-destructive' : 'bg-secondary/50 text-muted-foreground'
        )}>
          {result}
        </pre>
      )}

      {status === 'pending' && (
        <div className="flex gap-2 mt-3">
          <Button size="sm" className="h-7 text-xs" onClick={() => approve({ variables: { callId } }).catch(() => {})}>
            Approve
          </Button>
          <Button
            size="sm"
            variant="ghost"
            className="h-7 text-xs text-destructive hover:text-destructive"
            onClick={() => deny({ variables: { callId } }).catch(() => {})}
          >
            Deny
          </Button>
        </div>
      )}
    </div>
  )
}
