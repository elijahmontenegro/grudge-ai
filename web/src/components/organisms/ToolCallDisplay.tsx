import { gql } from '@apollo/client'
import { useMutation } from '@apollo/client/react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'

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
  const [approve] = useMutation<any>(APPROVE_TOOL)
  const [deny] = useMutation<any>(DENY_TOOL)

  return (
    <div className="border border-border rounded-lg p-3 bg-card text-sm">
      <div className="flex items-center gap-2 mb-2">
        <Badge variant={isError ? 'destructive' : 'secondary'}>{toolName}</Badge>
        <Badge variant="outline" className="text-xs">{status}</Badge>
      </div>

      <pre className="text-xs bg-secondary p-2 rounded overflow-auto max-h-32 font-mono">
        {args}
      </pre>

      {result && (
        <pre className="text-xs bg-secondary p-2 rounded overflow-auto max-h-48 mt-2 font-mono">
          {result}
        </pre>
      )}

      {status === 'pending' && (
        <div className="flex gap-2 mt-2">
          <Button size="sm" onClick={() => approve({ variables: { callId } })}>
            Approve
          </Button>
          <Button
            size="sm"
            variant="destructive"
            onClick={() => deny({ variables: { callId } })}
          >
            Deny
          </Button>
        </div>
      )}
    </div>
  )
}
