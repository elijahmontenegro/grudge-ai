import { gql } from '@apollo/client'
import { useQuery } from '@apollo/client/react'
import { useNavigate } from 'react-router'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'

const THREAD_BRANCHES = gql`
  query ThreadBranches($includeArchived: Boolean) {
    threads(includeArchived: $includeArchived) {
      id
      name
      parentThreadId
      branchPointPosition
    }
  }
`

interface Props {
  currentThreadId: string
}

export function BranchNavigator({ currentThreadId }: Props) {
  const navigate = useNavigate()
  const { data } = useQuery<{threads: any[]}>(THREAD_BRANCHES, {
    variables: { includeArchived: false },
  })

  const threads = data?.threads ?? []
  const current = threads.find((t: { id: string }) => t.id === currentThreadId)

  // Find parent and siblings
  const parent = current?.parentThreadId
    ? threads.find((t: { id: string }) => t.id === current.parentThreadId)
    : null
  const siblings = threads.filter(
    (t: { parentThreadId: string | null; id: string }) =>
      t.parentThreadId === current?.parentThreadId && t.id !== currentThreadId,
  )
  const children = threads.filter(
    (t: { parentThreadId: string | null }) => t.parentThreadId === currentThreadId,
  )

  if (!parent && siblings.length === 0 && children.length === 0) {
    return null
  }

  return (
    <div className="flex items-center gap-1 text-xs">
      {parent && (
        <Button
          variant="ghost"
          size="sm"
          className="h-6 text-xs"
          onClick={() => navigate(`/thread/${parent.id}`)}
        >
          ← {parent.name || 'Parent'}
        </Button>
      )}
      {siblings.map((s: { id: string; name: string; branchPointPosition: number | null }) => (
        <Button
          key={s.id}
          variant="outline"
          size="sm"
          className="h-6 text-xs"
          onClick={() => navigate(`/thread/${s.id}`)}
        >
          {s.name || 'Branch'}
          {s.branchPointPosition != null && (
            <Badge variant="secondary" className="ml-1 text-[10px]">@{s.branchPointPosition}</Badge>
          )}
        </Button>
      ))}
      {children.map((c: { id: string; name: string; branchPointPosition: number | null }) => (
        <Button
          key={c.id}
          variant="ghost"
          size="sm"
          className="h-6 text-xs"
          onClick={() => navigate(`/thread/${c.id}`)}
        >
          → {c.name || 'Fork'}
        </Button>
      ))}
    </div>
  )
}
