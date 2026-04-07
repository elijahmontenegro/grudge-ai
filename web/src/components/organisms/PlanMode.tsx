import { gql } from '@apollo/client'
import { useMutation } from '@apollo/client/react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { ScrollArea } from '@/components/ui/scroll-area'
import { useAgentState } from '@/hooks/useAgentState'

const ENTER_PLAN = gql`
  mutation EnterPlanMode($threadId: ID!) { enterPlanMode(threadId: $threadId) }
`

const APPROVE_PLAN = gql`
  mutation ApprovePlan($threadId: ID!, $executionMode: ExecutionMode!) {
    approvePlan(threadId: $threadId, executionMode: $executionMode)
  }
`


interface Props {
  threadId: string
  planContent?: string
}

export function PlanMode({ threadId, planContent }: Props) {
  const { state } = useAgentState(threadId)
  const [enterPlan] = useMutation<any>(ENTER_PLAN)
  const [approvePlan] = useMutation<any>(APPROVE_PLAN)

  const isPlanMode = state?.mode === 'PLAN'

  if (!isPlanMode && !planContent) {
    return (
      <Button
        variant="outline"
        size="sm"
        onClick={() => enterPlan({ variables: { threadId } })}
      >
        Enter Plan Mode
      </Button>
    )
  }

  if (isPlanMode && !planContent) {
    return (
      <div className="border border-border rounded-lg p-3 bg-card">
        <div className="flex items-center gap-2 mb-2">
          <Badge>Plan Mode</Badge>
          <span className="text-sm text-muted">Agent is exploring and designing...</span>
        </div>
        <p className="text-xs text-muted">
          Write tools disabled. The agent reads code and writes a plan.
        </p>
      </div>
    )
  }

  return (
    <div className="border border-border rounded-lg p-3 bg-card space-y-3">
      <div className="flex items-center gap-2">
        <Badge>Plan Ready</Badge>
      </div>

      <ScrollArea className="max-h-64">
        <pre className="text-sm whitespace-pre-wrap font-mono bg-secondary p-3 rounded">
          {planContent}
        </pre>
      </ScrollArea>

      <div className="flex gap-2">
        <Button size="sm" onClick={() => approvePlan({
          variables: { threadId, executionMode: 'MANUAL' },
        })}>
          Approve (Manual)
        </Button>
        <Button size="sm" variant="outline" onClick={() => approvePlan({
          variables: { threadId, executionMode: 'AUTONOMOUS' },
        })}>
          Approve (Autonomous)
        </Button>
      </div>
    </div>
  )
}
