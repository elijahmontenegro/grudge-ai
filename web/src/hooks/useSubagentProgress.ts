import { useEffect, useState } from 'react'
import { useSubscription } from '@apollo/client/react'
import { SUBAGENT_PROGRESS_SUB } from '@/graphql/operations'
import type {
  SubagentProgressSubSubscription,
  SubagentProgressSubSubscriptionVariables,
} from '@/graphql/generated/types'

export interface LiveSubagent {
  forkThreadId: string
  task: string
  status: string
  roundCount: number
}

export function useSubagentProgress(
  threadId: string,
  resetKey: number | string = 0,
): LiveSubagent[] {
  const [subagents, setSubagents] = useState<LiveSubagent[]>([])
  useSubscription<SubagentProgressSubSubscription, SubagentProgressSubSubscriptionVariables>(
    SUBAGENT_PROGRESS_SUB,
    {
      variables: { threadId },
      skip: !threadId,
      onData: ({ data }) => {
        const ev = data.data?.subagentProgress
        if (!ev) return
        setSubagents((cur) => {
          const existing = cur.find((s) => s.forkThreadId === ev.forkThreadId)
          if (existing) {
            return cur.map((s) =>
              s.forkThreadId === ev.forkThreadId
                ? { ...s, status: ev.status, roundCount: ev.roundCount }
                : s,
            )
          }
          return [
            ...cur,
            {
              forkThreadId: ev.forkThreadId,
              task: ev.task,
              status: ev.status,
              roundCount: ev.roundCount,
            },
          ]
        })
      },
    },
  )

  useEffect(() => {
    setSubagents([])
  }, [threadId, resetKey])

  return subagents
}
