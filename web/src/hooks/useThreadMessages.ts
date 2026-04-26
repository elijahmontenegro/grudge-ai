import { useMemo } from 'react'
import { useQuery } from '@apollo/client/react'
import { GET_THREAD_MESSAGES } from '@/graphql/operations'
import { adaptMessage } from '@/domain/adapters'
import type { GetThreadMessagesQuery, GetThreadMessagesQueryVariables } from '@/graphql/generated/types'
import type { Message, Thread } from '@/domain/types'

export interface ThreadMessagesResult {
  thread: (Thread & { workingDirs: string[]; sandboxed: boolean }) | null
  messages: Message[]
  loading: boolean
  error: string | null
}

export function useThreadMessages(threadId: string): ThreadMessagesResult {
  const { data, loading, error } = useQuery<
    GetThreadMessagesQuery,
    GetThreadMessagesQueryVariables
  >(GET_THREAD_MESSAGES, { variables: { threadId }, skip: !threadId })

  const thread: (Thread & { workingDirs: string[]; sandboxed: boolean }) | null = useMemo(() => {
    if (!data?.thread) return null
    const t = data.thread
    return {
      id: t.id,
      name: t.name?.trim() || 'Untitled',
      state: 'idle',
      lastActive: 'now',
      msgCount: t.messageCount,
      parentId: t.parentThreadId ?? undefined,
      branchAt: t.branchPointPosition ?? undefined,
      archived: t.archivedAt != null,
      workingDirs: [...t.workingDirs],
      sandboxed: t.sandboxed,
    }
  }, [data])

  const messages = useMemo<Message[]>(
    () => (data?.messages ? data.messages.map((m) => adaptMessage(m, threadId)) : []),
    [data, threadId],
  )

  return { thread, messages, loading, error: error?.message ?? null }
}
