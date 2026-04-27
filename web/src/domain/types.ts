// Plain types that don't shadow the GraphQL surface. Components that
// need wire-format Thread / Message consume the gql types directly
// (via @/hooks/useThreads.ThreadSummary and @/hooks/useThreadMessages.
// ThreadMessage). Computed view fields are in @/domain/derive.
//
// What lives here: types that are either purely UI (PaletteItem,
// User, Artifact view-model) or that aggregate gql data into a
// shape the wire doesn't carry (Artifact + ArtifactTouch).

export interface PaletteItem {
  kind: 'action' | 'thread' | 'skill' | 'search' | 'setting'
  label: string
  hint: string
}

export interface ActivityEntry {
  when: string
  what: string
  threadId?: string
  threadName?: string
}

// Artifact — a file the model touched via tool calls (read, wrote,
// edited, or deleted). Derived entirely from corpus tool-call /
// tool-result blocks; no new backend storage. The Artifacts panel
// shows one entry per unique path, most-recently-touched first.
// Plan.adoc is rendered specially.
export type ArtifactOp = 'read' | 'write' | 'edit' | 'delete'

export interface ArtifactTouch {
  op: ArtifactOp
  toolName: string
  toolCallId: string
  messageId: string
  position: number // message position — used to sort chronologically
  argsJSON: string // raw tool args (for display / debugging)
  /** For writes/edits: the content written. For reads: the returned content. */
  body?: string
  /** For edits: old_string → new_string, if the tool is FileEdit. */
  edit?: { oldString: string; newString: string }
  /** Whether the tool_result indicated an error. */
  isError?: boolean
}

export interface Artifact {
  path: string
  /** Latest op (determines the display state). If the file was deleted,
   *  op=delete even if there are prior writes. */
  latestOp: ArtifactOp
  /** Position of the latest touch — used to sort artifacts by recency. */
  latestPosition: number
  /** All touches in chronological order. */
  touches: ArtifactTouch[]
  /** True if the path ends in /plan.adoc or \plan.adoc — triggers
   *  AsciiDoc rendering + approve/reject/edit. */
  isPlan: boolean
}

export interface User {
  name: string
  handle: string
  initials: string
  host: string
}
