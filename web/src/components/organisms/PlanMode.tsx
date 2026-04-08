import { gql } from '@apollo/client'
import { useMutation } from '@apollo/client/react'
import { ScrollArea } from '@/components/ui/scroll-area'
import { useAgentState } from '@/hooks/useAgentState'

const ENTER_PLAN = gql`
  mutation EnterPlanMode($threadId: ID!) { enterPlanMode(threadId: $threadId) }
`

const EXIT_PLAN = gql`
  mutation StopAgent($threadId: ID!) { stopAgent(threadId: $threadId) }
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
  const [enterPlan] = useMutation<{enterPlanMode: boolean}>(ENTER_PLAN)
  const [exitPlan] = useMutation<{stopAgent: boolean}>(EXIT_PLAN)
  const [approvePlan] = useMutation<{approvePlan: boolean}>(APPROVE_PLAN)

  const isPlanMode = state?.mode === 'PLAN'

  // Planning in progress
  if (isPlanMode && !planContent) {
    return (
      <div className="rounded-xl bg-card p-4 mb-3 animate-fade-in">
        <div className="flex items-center gap-3 mb-2">
          <span className="h-2 w-2 rounded-full bg-primary animate-pulse-subtle shrink-0" />
          <span className="text-sm font-medium">Plan mode</span>
        </div>
        <p className="text-xs text-muted-foreground/40 mb-3">
          Write tools disabled. The agent reads code and designs an approach.
        </p>
        <button
          onClick={() => exitPlan({ variables: { threadId } })}
          className="px-3 py-1.5 rounded-lg text-xs text-muted-foreground/50 hover:text-muted-foreground hover:bg-foreground/[0.03] transition-colors"
        >
          Exit plan mode
        </button>
      </div>
    )
  }

  // Plan ready for review
  if (planContent) {
    return (
      <div className="rounded-xl bg-card p-4 mb-3 space-y-3 animate-fade-in">
        <div className="flex items-center gap-3">
          <span className="h-2 w-2 rounded-full bg-emerald-500 shrink-0" />
          <span className="text-sm font-medium">Plan ready</span>
        </div>
        <ScrollArea className="max-h-48">
          <pre className="text-xs whitespace-pre-wrap font-mono text-muted-foreground/60 bg-foreground/[0.02] p-3 rounded-lg">
            {planContent}
          </pre>
        </ScrollArea>
        <div className="flex gap-2">
          <button
            onClick={() => approvePlan({ variables: { threadId, executionMode: 'MANUAL' } })}
            className="px-4 py-1.5 rounded-lg text-xs font-medium bg-primary text-primary-foreground hover:bg-primary/90 transition-colors"
          >
            Execute manually
          </button>
          <button
            onClick={() => approvePlan({ variables: { threadId, executionMode: 'AUTONOMOUS' } })}
            className="px-4 py-1.5 rounded-lg text-xs font-medium bg-foreground/[0.05] hover:bg-foreground/[0.08] transition-colors"
          >
            Execute autonomously
          </button>
        </div>
      </div>
    )
  }

  // Trigger — proper button with icon and description
  return (
    <button
      onClick={() => enterPlan({ variables: { threadId } })}
      className="flex items-center gap-3 px-3 py-2 rounded-lg hover:bg-foreground/[0.03] transition-colors text-left group"
    >
      <span className="h-7 w-7 rounded-lg bg-primary/10 flex items-center justify-center text-primary shrink-0 group-hover:bg-primary/20 transition-colors">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/><polyline points="14 2 14 8 20 8"/><line x1="16" y1="13" x2="8" y2="13"/><line x1="16" y1="17" x2="8" y2="17"/></svg>
      </span>
      <div>
        <div className="text-xs font-medium text-muted-foreground group-hover:text-foreground transition-colors">Plan</div>
        <div className="text-[10px] text-muted-foreground/30">Read-only exploration first</div>
      </div>
    </button>
  )
}
