// Pure functions that derive UI-display shapes from raw gql shapes.
// Lives here so components can consume generated types directly while
// still getting "lastActive" / "running" / paired tool calls without
// re-implementing the math.

import {
  AgentMode,
  AgentStatus,
  type GetThreadMessagesQuery,
  type ListThreadsQuery,
} from '@/graphql/generated/types'

export type ThreadState = 'running' | 'idle' | 'paused'

/** Maps gql AgentStatus to the UI's ternary state vocabulary. */
export function gqlStatusToState(status: AgentStatus | undefined | null): ThreadState {
  switch (status) {
    case AgentStatus.Running:
      return 'running'
    case AgentStatus.Paused:
      return 'paused'
    default:
      return 'idle'
  }
}

/** True when the agent is in autonomous mode. Used by sidebar rows
 *  to show a long-form-run indicator. */
export function isAutonomous(mode: AgentMode | undefined | null): boolean {
  return mode === AgentMode.Autonomous
}

/** "5m" / "yesterday" / "3w" — relative time elapsed since createdAt.
 *  No localization; matches the prototype's terse style. */
export function formatRelative(ms: number): string {
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

/** Convenience: how long ago was a wire-format DateTime string. */
export function lastActiveOf(createdAt: string): string {
  return formatRelative(Date.now() - new Date(createdAt).getTime())
}

/** Surface name with the standard fallback. The backend stores empty
 *  string for unmaterialised threads. */
export function threadDisplayName(name: string | undefined | null): string {
  return name?.trim() || 'Untitled'
}

/** Gql Thread shape from ListThreads. Re-exported as a convenience
 *  so consumers don't have to repeat the indexed-access type. */
export type GqlThread = ListThreadsQuery['threads'][number]
export type GqlMessage = GetThreadMessagesQuery['messages'][number]

export interface PairedToolCall {
  id: string
  name: string
  arguments: string
  result: string | null
  status: 'ok' | 'running' | 'error'
}

/** Pair a message's toolCalls with their toolResults by callId. The
 *  agent commonly emits the call and its result in separate Message
 *  rows; consumers want them as one row at render time. */
export function pairToolCalls(m: GqlMessage): PairedToolCall[] {
  const resultByCallId: Record<string, string> = {}
  for (const r of m.toolResults) resultByCallId[r.toolCallId] = r.content
  return m.toolCalls.map((tc) => {
    const result = resultByCallId[tc.id] ?? null
    return {
      id: tc.id,
      name: tc.name,
      arguments: tc.arguments,
      result,
      status: result === null ? 'running' : 'ok',
    }
  })
}

/** Pair tool calls and results across an array of messages — the
 *  agent commonly stores the call in one Message row and its result
 *  in the next. Consumers want them paired for the whole turn. */
export function pairToolCallsAcross(messages: GqlMessage[]): Map<string, PairedToolCall> {
  const out = new Map<string, PairedToolCall>()
  // First pass: gather every result by call id.
  const resultByCallId: Record<string, string> = {}
  for (const m of messages) {
    for (const r of m.toolResults) resultByCallId[r.toolCallId] = r.content
  }
  // Second pass: emit one entry per call, with its (possibly later)
  // result.
  for (const m of messages) {
    for (const tc of m.toolCalls) {
      if (out.has(tc.id)) continue
      const result = resultByCallId[tc.id] ?? null
      out.set(tc.id, {
        id: tc.id,
        name: tc.name,
        arguments: tc.arguments,
        result,
        status: result === null ? 'running' : 'ok',
      })
    }
  }
  return out
}

/** Role normalisation. Backend ships strings; the UI cares about
 *  user/assistant/system. */
export type DisplayRole = 'user' | 'assistant' | 'system'

export function displayRole(role: string): DisplayRole {
  if (role === 'user') return 'user'
  if (role === 'system') return 'system'
  return 'assistant'
}
