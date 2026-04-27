import { useMemo } from 'react'
import { useQuery } from '@apollo/client/react'
import { GET_THREAD_MESSAGES } from '@/graphql/operations'
import type {
  GetThreadMessagesQuery,
  GetThreadMessagesQueryVariables,
} from '@/graphql/generated/types'

export type ThreadDetail = NonNullable<GetThreadMessagesQuery['thread']>
export type ThreadMessage = GetThreadMessagesQuery['messages'][number]

export interface ThreadMessagesResult {
  thread: ThreadDetail | null
  messages: ThreadMessage[]
  loading: boolean
  error: string | null
}

/** Returns the gql GetThreadMessages payload directly. Computed view
 *  fields (lastActive, paired tool calls) are derived at consumer
 *  sites via helpers in @/domain/derive. */
export function useThreadMessages(threadId: string): ThreadMessagesResult {
  const { data, loading, error } = useQuery<
    GetThreadMessagesQuery,
    GetThreadMessagesQueryVariables
  >(GET_THREAD_MESSAGES, { variables: { threadId }, skip: !threadId })

  const messages = useMemo<ThreadMessage[]>(
    () => (data?.messages ? [...data.messages] : []),
    [data],
  )

  return {
    thread: data?.thread ?? null,
    messages,
    loading,
    error: error?.message ?? null,
  }
}
