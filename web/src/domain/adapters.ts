import {
  AgentMode,
  AgentStatus,
  type GetRecentActivityQuery,
  type GetThreadMessagesQuery,
  type ListThreadsQuery,
} from '@/graphql/generated/types'
import type { ActivityEntry, Message, Thread, ThreadState } from './types'

function gqlStatusToState(status: AgentStatus | undefined | null): ThreadState {
  switch (status) {
    case AgentStatus.Running:
      return 'running'
    case AgentStatus.Paused:
      return 'paused'
    default:
      return 'idle'
  }
}

export function adaptThread(t: ListThreadsQuery['threads'][number]): Thread {
  const ageMs = Date.now() - new Date(t.createdAt).getTime()
  const lastActive = formatRelative(ageMs)
  // status / mode now live on the Thread schema directly — surfaced
  // here so the sidebar's running tick reads from Apollo cache rather
  // than the prior module-scope event bus.
  const isAutonomous = t.mode === AgentMode.Autonomous
  return {
    id: t.id,
    // Backend stores empty string for threads that haven't received a first
    // message yet. Surfacing "Untitled" is more useful than an invisible
    // sidebar row.
    name: t.name?.trim() || 'Untitled',
    state: gqlStatusToState(t.status),
    elapsed: isAutonomous ? 'autonomous' : undefined,
    lastActive,
    msgCount: t.messageCount,
    archived: t.archivedAt != null,
    parentId: t.parentThreadId ?? undefined,
    branchAt: t.branchPointPosition ?? undefined,
    workingDirs: t.workingDirs ?? [],
    sandboxed: t.sandboxed ?? false,
  }
}

function formatRelative(ms: number): string {
  const s = ms / 1000
  if (s < 60) return 'now'
  const m = s / 60
  if (m < 60) return `${Math.floor(m)}m`
  const h = m / 60
  if (h < 24) return `${Math.floor(h)}h`
  const d = h / 24
  if (d < 2) return 'yesterday'
  if (d < 7) return `${Math.floor(d)}d`
  if (d < 30) return `${Math.floor(d / 7)}w`
  return `${Math.floor(d / 30)}mo`
}

type BackendMessage = GetThreadMessagesQuery['messages'][number]

export function adaptMessage(m: BackendMessage, threadId: string): Message {
  const resultByCallId: Record<string, string> = {}
  for (const r of m.toolResults) {
    resultByCallId[r.toolCallId] = r.content
  }
  const tools = m.toolCalls.map((tc) => {
    const result = resultByCallId[tc.id] ?? null
    return {
      id: tc.id,
      name: tc.name,
      arguments: tc.arguments,
      result,
      status: result === null ? 'running' : 'ok',
    } as const
  })
  return {
    id: m.id,
    pos: m.position,
    role: m.role === 'user' ? 'user' : 'assistant',
    thread: threadId,
    text: m.content,
    thinking: m.thinking ?? undefined,
    tools: tools.length > 0 ? tools : undefined,
    toolResults:
      m.toolResults.length > 0
        ? m.toolResults.map((r) => ({ toolCallId: r.toolCallId, content: r.content }))
        : undefined,
    attachments:
      m.attachments && m.attachments.length > 0
        ? m.attachments.map((a) => ({
            id: a.id,
            filename: a.filename,
            mimeType: a.mimeType,
            sizeBytes: a.sizeBytes,
            path: a.path,
          }))
        : undefined,
  }
}

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
