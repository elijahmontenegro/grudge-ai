import { useState, useRef, useEffect } from 'react'
import { gql } from '@apollo/client'
import { useQuery } from '@apollo/client/react'
import { useNavigate } from 'react-router'

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
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  const { data } = useQuery<{threads: {id: string; name: string; parentThreadId: string | null; branchPointPosition: number | null}[]}>(THREAD_BRANCHES, {
    variables: { includeArchived: false },
  })

  const threads = data?.threads ?? []
  const current = threads.find((t) => t.id === currentThreadId)

  const parent = current?.parentThreadId
    ? threads.find((t) => t.id === current.parentThreadId)
    : null
  const siblings = current?.parentThreadId
    ? threads.filter((t) => t.parentThreadId === current.parentThreadId && t.id !== currentThreadId)
    : []
  const children = threads.filter(
    (t) => t.parentThreadId === currentThreadId,
  )

  // Close on outside click
  useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open])

  const total = (parent ? 1 : 0) + siblings.length + children.length
  if (total === 0) return null

  return (
    <div ref={ref} className="relative">
      <button
        onClick={() => setOpen(!open)}
        className="text-xs text-muted-foreground hover:text-foreground px-2 py-1 rounded-md hover:bg-secondary transition-colors"
      >
        {children.length > 0
          ? `${children.length} branch${children.length !== 1 ? 'es' : ''}`
          : parent
            ? 'branched'
            : `${siblings.length} sibling${siblings.length !== 1 ? 's' : ''}`}
      </button>

      {open && (
        <div className="absolute top-full right-0 mt-1 bg-card border border-border rounded-lg shadow-lg py-1 z-50 min-w-[180px] animate-fade-in">
          {parent && (
            <button
              onClick={() => { navigate(`/thread/${parent.id}`); setOpen(false) }}
              className="w-full text-left px-3 py-1.5 text-xs hover:bg-secondary transition-colors truncate"
            >
              <span className="text-muted-foreground mr-1">&larr;</span>
              {parent.name || 'Parent thread'}
            </button>
          )}
          {siblings.length > 0 && parent && <div className="h-px bg-border my-1" />}
          {siblings.map((s) => (
            <button
              key={s.id}
              onClick={() => { navigate(`/thread/${s.id}`); setOpen(false) }}
              className="w-full text-left px-3 py-1.5 text-xs hover:bg-secondary transition-colors truncate"
            >
              {s.name || 'Branch'}
              {s.branchPointPosition != null && (
                <span className="text-muted-foreground ml-1">@{s.branchPointPosition}</span>
              )}
            </button>
          ))}
          {children.length > 0 && (parent || siblings.length > 0) && <div className="h-px bg-border my-1" />}
          {children.map((c) => (
            <button
              key={c.id}
              onClick={() => { navigate(`/thread/${c.id}`); setOpen(false) }}
              className="w-full text-left px-3 py-1.5 text-xs hover:bg-secondary transition-colors truncate"
            >
              <span className="text-muted-foreground mr-1">&rarr;</span>
              {c.name || 'Fork'}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
