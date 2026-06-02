import { useEffect, useRef, useState } from 'react'
import { useLocalStorage } from '@/primitives/useLocalStorage'
import { AgentStatus } from '@/graphql/generated/types'
import { useAttachments, type AttachmentMeta } from '@/hooks/useAttachments'
import { Attachments } from './Attachments'
import { Foot } from './Foot'
import { Status } from './Status'
import { Workdirs } from './Workdirs'
import type {
  AgentRetryStatus,
  AutonomousDuration,
  ComposerMode,
  ComposerScope,
} from './types'

export type { AgentRetryStatus, AutonomousDuration, ComposerMode, ComposerScope } from './types'

interface ComposerProps {
  /** Keyed storage id for per-thread drafts. Typically the thread id. */
  draftKey?: string
  /** Thread id used by the attachment upload endpoint. When undefined
   *  (draft thread pre-first-send) attachment picker is disabled. */
  threadId?: string
  onSend: (text: string, attachments?: AttachmentMeta[]) => void
  onStartAutonomous?: (text: string, duration: AutonomousDuration, attachments?: AttachmentMeta[]) => void
  scope: ComposerScope
  setScope: (s: ComposerScope) => void
  mode: ComposerMode
  setMode: (m: ComposerMode) => void
  duration?: AutonomousDuration
  setDuration?: (d: AutonomousDuration) => void
  streaming: boolean
  // Agent runtime status — folded into the composer so the running
  // indicator and its controls (pause/resume/stop, correction-on-
  // resume) live adjacent to where the user types. The topbar stays
  // clean.
  agentStatus?: AgentStatus
  agentIsAutonomous?: boolean
  agentElapsed?: string | null
  onPauseAgent?: () => void
  onResumeAgent?: (correction?: string) => void
  onStopAgent?: () => void
  agentControlsPending?: boolean
  /** Transient-failure retry state. Null when the LLM call is not
   *  being retried. Rendered as a warning-tinted strip above the
   *  runtime status row. */
  agentRetry?: AgentRetryStatus | null
  /** Working directories to be mounted in the new thread's sandbox.
   *  Supplied by the Home/new-thread composer so the user can pick
   *  which paths the agent sees before the thread is created. Once a
   *  thread exists, working dirs live in ThreadConfigPopover and are
   *  edited there — don't pass these props on established threads. */
  workingDirs?: string[]
  onWorkingDirsChange?: (dirs: string[]) => void
  /** Sandboxed toggle for thread creation. When provided alongside a
   *  setter, the composer shows a sandbox switch next to the mount-dir
   *  row. Same rationale as workingDirs: only exposed pre-creation;
   *  established threads use ThreadConfigPopover. */
  sandboxed?: boolean
  onSandboxedChange?: (v: boolean) => void
}

const DRAFT_PREFIX = 'grudge.draft:'

const stringCodec = {
  serialize: (v: string) => v,
  parse: (raw: string) => raw,
}

/**
 * Composer shell. Owns: textarea state + per-thread draft sync,
 * attachment-upload hook, drag/drop wiring, submit logic, key
 * handlers. Composes four presenters around it: Status (retry strip
 * + running/paused/stop row), Workdirs (pre-creation chip list +
 * sandbox toggle), Attachments (pending-upload chip strip), Foot
 * (attach btn + mode/scope/duration selectors + send).
 */
export function Composer({
  draftKey,
  threadId,
  onSend,
  onStartAutonomous,
  scope,
  setScope,
  mode,
  setMode,
  duration: durationProp,
  setDuration: setDurationProp,
  streaming,
  agentStatus = AgentStatus.Idle,
  agentIsAutonomous = false,
  agentElapsed,
  onPauseAgent,
  onResumeAgent,
  onStopAgent,
  agentControlsPending = false,
  agentRetry = null,
  workingDirs,
  onWorkingDirsChange,
  sandboxed,
  onSandboxedChange,
}: ComposerProps) {
  // Per-thread draft text persists across tab close. useLocalStorage
  // handles the dynamic-key dance — when draftKey switches (the user
  // navigates to another thread) the load effect refreshes value from
  // the new key BEFORE the persist effect could overwrite it. Null
  // key (pre-thread-creation composer on Home) → in-memory only, no
  // localStorage write.
  const [value, setValue] = useLocalStorage<string>(
    draftKey ? DRAFT_PREFIX + draftKey : null,
    '',
    stringCodec,
    { removeOnEmpty: true },
  )
  const [durationLocal, setDurationLocal] = useState<AutonomousDuration>('1h')
  const duration = durationProp ?? durationLocal
  const setDuration = setDurationProp ?? setDurationLocal
  const taRef = useRef<HTMLTextAreaElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [dragging, setDragging] = useState(false)
  const dragDepthRef = useRef(0)

  const attachmentsEnabled = !!threadId
  const { pending, upload, remove, clear, metaForSend } = useAttachments(threadId ?? '')

  const running = agentStatus === AgentStatus.Running
  const paused = agentStatus === AgentStatus.Paused
  const runtimeActive = running || paused

  useEffect(() => {
    if (taRef.current) {
      taRef.current.style.height = 'auto'
      taRef.current.style.height = Math.min(240, taRef.current.scrollHeight) + 'px'
    }
  }, [value])

  // True iff at least one attachment finished uploading. Text may be
  // empty when the user is shipping pure-attachment prompts ("look
  // at this file"), so the send gate is `text.trim() || haveDoneAttach`.
  const haveDoneAttach = pending.some((x) => x.status === 'done' && x.meta)
  const anyUploading = pending.some((x) => x.status === 'uploading')

  function submit(overrideAutonomous = false) {
    const text = value.trim()
    if (!text && !haveDoneAttach) return
    // Block submission while uploads are still in flight — sending
    // now would drop the pending files from the turn.
    if (anyUploading) return
    const attachments = haveDoneAttach ? metaForSend() : undefined
    // When paused, the submit button is Resume — pressing it sends
    // the typed text as a correction, or resumes with no correction
    // if empty. Paused resume is text-only; attachments are a
    // new-turn concept.
    if (paused && onResumeAgent) {
      setValue('')
      onResumeAgent(text || undefined)
      return
    }
    if (streaming || running) return
    const goAutonomous = overrideAutonomous || mode === 'autonomous'
    if (goAutonomous && onStartAutonomous) {
      onStartAutonomous(text, duration, attachments)
      if (overrideAutonomous && mode !== 'autonomous') setMode('autonomous')
    } else {
      onSend(text, attachments)
    }
    setValue('')
    clear()
  }

  function openFilePicker() {
    if (!attachmentsEnabled) return
    fileInputRef.current?.click()
  }

  function onFileInputChange(e: React.ChangeEvent<HTMLInputElement>) {
    const files = Array.from(e.target.files ?? [])
    if (files.length) void upload(files)
    // Reset so picking the same file twice re-fires change.
    e.target.value = ''
  }

  function onPaste(e: React.ClipboardEvent<HTMLTextAreaElement>) {
    if (!attachmentsEnabled) return
    const items = e.clipboardData?.items
    if (!items) return
    const files: File[] = []
    for (let i = 0; i < items.length; i++) {
      const it = items[i]
      if (it.kind === 'file') {
        const f = it.getAsFile()
        if (f) files.push(f)
      }
    }
    if (files.length === 0) return
    // Only intercept the paste when there's at least one file —
    // text paste must fall through to the textarea's default
    // handler.
    e.preventDefault()
    void upload(files)
  }

  function onDragEnter(e: React.DragEvent<HTMLDivElement>) {
    if (!attachmentsEnabled) return
    if (!Array.from(e.dataTransfer.types ?? []).includes('Files')) return
    dragDepthRef.current++
    setDragging(true)
  }

  function onDragLeave() {
    if (!attachmentsEnabled) return
    dragDepthRef.current = Math.max(0, dragDepthRef.current - 1)
    if (dragDepthRef.current === 0) setDragging(false)
  }

  function onDragOver(e: React.DragEvent<HTMLDivElement>) {
    if (!attachmentsEnabled) return
    if (!Array.from(e.dataTransfer.types ?? []).includes('Files')) return
    e.preventDefault()
    e.dataTransfer.dropEffect = 'copy'
  }

  function onDrop(e: React.DragEvent<HTMLDivElement>) {
    if (!attachmentsEnabled) return
    e.preventDefault()
    dragDepthRef.current = 0
    setDragging(false)
    const files = Array.from(e.dataTransfer.files ?? [])
    if (files.length) void upload(files)
  }

  function onKey(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key !== 'Enter') return
    // Shift+Enter inserts a newline (default browser behavior).
    if (e.shiftKey && !(e.metaKey || e.ctrlKey)) return
    e.preventDefault()
    // Stop the window-level shortcut handler from also flipping mode
    // after we've already handled the key here.
    e.stopPropagation()
    // Cmd/Ctrl+Shift+Enter — submit as autonomous regardless of
    // current mode. Plain Enter and Cmd/Ctrl+Enter both submit
    // under the current mode.
    submit(e.metaKey || e.ctrlKey ? e.shiftKey : false)
  }

  const placeholder = paused
    ? 'Paused — type a correction then press Resume, or leave empty to resume as-is.'
    : running
      ? agentIsAutonomous
        ? 'Autonomous run in progress. Pause to intervene.'
        : 'Agent is working. Pause to intervene.'
      : streaming
        ? 'Agent is working…'
        : mode === 'plan'
          ? 'Describe what you want. The agent will propose a plan first.'
          : mode === 'autonomous'
            ? 'Goal for the autonomous run. ↵ to start, ⇧↵ for newline.'
            : 'Continue the thread. ↵ to send, ⇧↵ for newline.'

  // Textarea lock semantics:
  //   running  → locked (correction must go through pause first)
  //   paused   → unlocked (typed text becomes the correction on Resume)
  //   streaming without agentStatus (older fallback) → locked
  const textareaDisabled = running || (streaming && !paused)

  const sendLabel = paused
    ? value.trim() ? 'correct & resume' : 'resume'
    : mode === 'autonomous'
      ? 'start run'
      : mode === 'plan'
        ? 'request plan'
        : 'send'

  // Show the status strip (with stop) during any in-flight work —
  // autonomous runs (agentStatus=Running/Paused) AND normal streaming
  // turns. Previously the strip was gated on runtimeActive alone, so
  // a regular chat turn had no way to interrupt.
  const statusVisible = runtimeActive || streaming

  const sendDisabled = paused
    ? agentControlsPending
    : running || streaming || anyUploading || (!value.trim() && !haveDoneAttach)

  return (
    <div className="composer">
      <div
        className="composer-shell"
        data-mode={mode}
        data-runtime={runtimeActive ? 'true' : undefined}
        data-dragging={dragging ? 'true' : undefined}
        onDragEnter={onDragEnter}
        onDragLeave={onDragLeave}
        onDragOver={onDragOver}
        onDrop={onDrop}
      >
        <Status
          visible={statusVisible}
          paused={paused}
          running={running}
          isAutonomous={agentIsAutonomous}
          elapsed={agentElapsed ?? null}
          retry={agentRetry}
          hasPendingResumeText={!!value.trim()}
          pending={agentControlsPending}
          onPause={onPauseAgent}
          onResume={onResumeAgent}
          onStop={onStopAgent}
        />
        <Workdirs
          workingDirs={workingDirs}
          onWorkingDirsChange={onWorkingDirsChange}
          sandboxed={sandboxed}
          onSandboxedChange={onSandboxedChange}
        />
        <Attachments pending={pending} onRemove={remove} />
        <textarea
          ref={taRef}
          className="composer-input"
          placeholder={placeholder}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={onKey}
          onPaste={onPaste}
          disabled={textareaDisabled}
          rows={2}
        />
        <Foot
          attachmentsEnabled={attachmentsEnabled}
          fileInputRef={fileInputRef}
          onFileInputChange={onFileInputChange}
          openFilePicker={openFilePicker}
          runtimeActive={runtimeActive}
          paused={paused}
          mode={mode}
          setMode={setMode}
          scope={scope}
          setScope={setScope}
          duration={duration}
          setDuration={setDuration}
          valueLength={value.length}
          sendLabel={sendLabel}
          sendDisabled={sendDisabled}
          onSubmit={() => submit()}
        />
      </div>
    </div>
  )
}
