export type Role = 'user' | 'assistant'

export interface MessageToolInvocation {
  id: string
  name: string
  arguments: string
  result: string | null
  status: 'ok' | 'running' | 'error'
}

/** A tool result block not yet paired with its call. Backend commonly emits
 *  tool calls and their results in separate Message rows — consumers pair
 *  them at render time across the full turn. */
export interface MessageToolResult {
  toolCallId: string
  content: string
}

/** Reference-only shape for rendering uploaded files on a message. The
 *  bytes live on disk in the thread workspace; clients fetch via the
 *  HTTP endpoint. */
export interface MessageAttachment {
  id: string
  filename: string
  mimeType: string
  sizeBytes: number
  path: string
}

export interface Message {
  id: string
  pos: number
  role: Role
  thread: string
  text: string
  thinking?: string
  tools?: MessageToolInvocation[]
  toolResults?: MessageToolResult[]
  attachments?: MessageAttachment[]
}

export type SelectionSource = 'cross-encoder'

export interface SelectionItem {
  id: string
  score: number
  source: SelectionSource
  hop: number
  ce: number
  temp: number
  phantom?: boolean
  crossThread?: boolean
  threadName?: string
  snippet?: string
}

export interface ExclusionItem {
  id: string
  reason: string
  score: number
}

export interface Selection {
  selected: SelectionItem[]
  excluded: ExclusionItem[]
}

export interface ToolInvocation {
  name: string
  args: string
  result: string
  status: 'ok' | 'running' | 'error'
}

export type ThreadState = 'running' | 'idle' | 'paused'

export interface Thread {
  id: string
  name: string
  state: ThreadState
  pinned?: boolean
  archived?: boolean
  lastActive: string
  elapsed?: string
  msgCount: number
  corpus?: Message[]
  parentId?: string
  branchAt?: number
  /** Host directories mounted into the sandbox for this thread. Empty
   *  when sandboxed=false or the user hasn't added any. */
  workingDirs?: string[]
  /** When true, Bash + file tools run inside the Docker workspace.
   *  Source of truth for the Topbar's thread-config popover checkbox. */
  sandboxed?: boolean
}

export type PaletteKind = 'action' | 'thread' | 'skill' | 'search' | 'setting'

export interface PaletteItem {
  kind: PaletteKind
  label: string
  hint: string
}

export interface ActivityEntry {
  when: string
  what: React.ReactNode
  threadId?: string
  threadName?: string
}

// Artifact — a file the model touched via tool calls (read, wrote, edited,
// or deleted). Derived entirely from corpus tool_call/tool_result blocks;
// no new backend storage. The Artifacts panel shows one entry per unique
// path, most-recently-touched first. Plan.adoc is rendered specially.
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
  /** Latest op (determines the display state). If the file was deleted, op=delete even if there are prior writes. */
  latestOp: ArtifactOp
  /** Position of the latest touch — used to sort artifacts by recency. */
  latestPosition: number
  /** All touches in chronological order. */
  touches: ArtifactTouch[]
  /** True if the path ends in /plan.adoc or \plan.adoc — triggers AsciiDoc rendering + approve/reject/edit. */
  isPlan: boolean
}

export interface User {
  name: string
  handle: string
  initials: string
  host: string
}
