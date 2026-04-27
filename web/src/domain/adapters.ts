// adaptThread / adaptMessage are gone — components consume the gql
// types directly via @/hooks/useThreads.ThreadSummary and
// @/hooks/useThreadMessages.ThreadMessage. Computed view fields
// (lastActive, paired tool calls, autonomous indicator) live in
// @/domain/derive.
//
// adaptActivity stays because the GetRecentActivity payload pairs
// a wire-format ISO timestamp with a UI-friendly summary; the
// adapter computes the relative when-string. ActivityEntry is a
// pure UI shape, not a wire-format shadow.

import type { GetRecentActivityQuery } from '@/graphql/generated/types'
import { formatRelative } from './derive'
import type { ActivityEntry } from './types'

type BackendActivity = GetRecentActivityQuery['recentActivity'][number]

export function adaptActivity(a: BackendActivity): ActivityEntry {
  const when = formatRelative(Date.now() - new Date(a.timestamp).getTime())
  return {
    when,
    what: a.summary,
    threadId: a.threadId || undefined,
    threadName: a.threadName || undefined,
  }
}
