import type { Artifact, ArtifactOp, ArtifactTouch, Message } from './types'

// Tools we currently track as file touches. Bash touches (rm, mv, > foo)
// are not tracked — doing so would require parsing arbitrary shell and
// misattributing on heuristic failures. Only structured file tools.
const FILE_TOOLS: Record<string, ArtifactOp> = {
  FileRead: 'read',
  FileWrite: 'write',
  FileEdit: 'edit',
  NotebookEdit: 'edit',
}

function extractPath(_toolName: string, argsJSON: string): string | null {
  try {
    const args = JSON.parse(argsJSON) as Record<string, unknown>
    // FileRead / FileWrite / FileEdit use `path`.
    // NotebookEdit uses `notebook_path`.
    const raw =
      (typeof args.path === 'string' && args.path) ||
      (typeof args.notebook_path === 'string' && args.notebook_path) ||
      null
    return raw
  } catch {
    return null
  }
}

function bodyForWrite(toolName: string, argsJSON: string): string | undefined {
  try {
    const args = JSON.parse(argsJSON) as Record<string, unknown>
    if (toolName === 'FileWrite' && typeof args.content === 'string') return args.content
    // FileEdit doesn't carry full content — handled separately via edit field.
    return undefined
  } catch {
    return undefined
  }
}

function editForTool(
  toolName: string,
  argsJSON: string,
): { oldString: string; newString: string } | undefined {
  if (toolName !== 'FileEdit') return undefined
  try {
    const args = JSON.parse(argsJSON) as Record<string, unknown>
    const oldString = typeof args.old_string === 'string' ? args.old_string : ''
    const newString = typeof args.new_string === 'string' ? args.new_string : ''
    return { oldString, newString }
  } catch {
    return undefined
  }
}

// Very loose error heuristic — same pattern used in Turn.tsx's TurnResponses.
// Backend doesn't yet expose an isError flag on ToolResultBlock.
function looksLikeError(content: string): boolean {
  return (
    /^\s*(error|failed|exception|traceback)/i.test(content) ||
    /\b(connection refused|timed? out|not found|permission denied|no such file|actively refused)\b/i.test(
      content,
    )
  )
}

function isPlanPath(path: string): boolean {
  // Match any `...plan.adoc` at end of path, forward- or back-slash separated.
  return /[\\/]plan\.adoc$/i.test(path)
}

/**
 * Walk the thread's corpus and aggregate tool-driven file touches into a
 * list of Artifact entries. One entry per unique path. Touches in
 * chronological order, latest-op on the entry reflects current disk state.
 *
 * Note: corpus already pairs ToolCall messages with ToolResult messages
 * via `corpus[i].toolResults[j].toolCallId`. Tool calls split across
 * multiple messages in the same turn — result may be in a later message
 * than the call. First pass builds a call-id → result map spanning all
 * messages, second pass walks calls in order and emits touches.
 */
export function aggregateArtifacts(corpus: Message[]): Artifact[] {
  // Build a global call-id → result map across the whole corpus.
  const resultByCallId = new Map<string, string>()
  for (const msg of corpus) {
    for (const r of msg.toolResults ?? []) {
      // First result wins — subsequent ones are usually just the same call
      // re-reported in a later message.
      if (!resultByCallId.has(r.toolCallId)) {
        resultByCallId.set(r.toolCallId, r.content)
      }
    }
  }

  const byPath = new Map<string, Artifact>()

  for (const msg of corpus) {
    for (const call of msg.tools ?? []) {
      const op = FILE_TOOLS[call.name]
      if (!op) continue
      const path = extractPath(call.name, call.arguments)
      if (!path) continue

      const resultContent = resultByCallId.get(call.id)
      const body =
        op === 'read'
          ? resultContent
          : op === 'write'
            ? bodyForWrite(call.name, call.arguments)
            : undefined

      const touch: ArtifactTouch = {
        op,
        toolName: call.name,
        toolCallId: call.id,
        messageId: msg.id,
        position: msg.pos,
        argsJSON: call.arguments,
        body,
        edit: editForTool(call.name, call.arguments),
        isError: resultContent != null ? looksLikeError(resultContent) : undefined,
      }

      const existing = byPath.get(path)
      if (existing) {
        existing.touches.push(touch)
        if (touch.position >= existing.latestPosition) {
          existing.latestOp = touch.op
          existing.latestPosition = touch.position
        }
      } else {
        byPath.set(path, {
          path,
          latestOp: touch.op,
          latestPosition: touch.position,
          touches: [touch],
          isPlan: isPlanPath(path),
        })
      }
    }
  }

  // Most recently touched first.
  return Array.from(byPath.values()).sort((a, b) => b.latestPosition - a.latestPosition)
}
