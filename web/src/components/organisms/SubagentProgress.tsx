import { gql } from '@apollo/client'
import { useSubscription } from '@apollo/client/react'
import { useNavigate } from 'react-router'
import { cn } from '@/lib/utils'

const SUBAGENT_SUBSCRIPTION = gql`
  subscription SubagentProgress($threadId: ID!) {
    subagentProgress(threadId: $threadId) {
      threadId
      forkThreadId
      task
      status
      roundCount
    }
  }
`

interface SubagentData {
  threadId: string
  forkThreadId: string
  task: string
  status: string
  roundCount: number
}

interface Props {
  threadId: string
}

export function SubagentProgress({ threadId }: Props) {
  const navigate = useNavigate()
  const { data } = useSubscription<{ subagentProgress: SubagentData }>(
    SUBAGENT_SUBSCRIPTION,
    { variables: { threadId } },
  )

  const sub = data?.subagentProgress
  if (!sub) return null

  return (
    <div className="py-2 animate-fade-in">
      <div className="flex items-center gap-3 text-xs">
        <div className="flex items-center gap-1.5">
          <span className={cn(
            'inline-block w-1.5 h-1.5 rounded-full',
            sub.status === 'running' ? 'bg-primary animate-pulse-subtle' : 'bg-muted-foreground/30'
          )} />
          <span className="font-medium">Subagent</span>
        </div>
        <span className="text-muted-foreground">Round {sub.roundCount}</span>
        <span className="text-muted-foreground truncate max-w-[200px]">{sub.task}</span>
        <button
          onClick={() => navigate(`/thread/${sub.forkThreadId}`)}
          className="text-primary hover:underline ml-auto shrink-0"
        >
          View fork
        </button>
      </div>
    </div>
  )
}
