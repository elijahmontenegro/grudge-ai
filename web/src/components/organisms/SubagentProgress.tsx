import { gql } from '@apollo/client'
import { useSubscription } from '@apollo/client/react'
import { Badge } from '@/components/ui/badge'
import { useNavigate } from 'react-router'

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
    <div className="border border-border rounded-lg p-3 bg-card text-sm">
      <div className="flex items-center gap-2 mb-1">
        <Badge variant={sub.status === 'running' ? 'default' : 'secondary'} className="text-xs">
          Subagent
        </Badge>
        <Badge variant="outline" className="text-xs">{sub.status}</Badge>
        <Badge variant="outline" className="text-xs">Round {sub.roundCount}</Badge>
      </div>
      <p className="text-xs text-muted mb-2 line-clamp-2">{sub.task}</p>
      <button
        onClick={() => navigate(`/thread/${sub.forkThreadId}`)}
        className="text-xs text-primary hover:underline"
      >
        View fork →
      </button>
    </div>
  )
}
