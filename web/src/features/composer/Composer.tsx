import { useEffect, useRef, useState } from 'react'
import { IconPaperclip, IconPause, IconPlay, IconPlus, IconSpinner, IconStop, IconX } from '@/primitives/icons'
import { IS_MAC } from '@/primitives/platform'
import { AgentStatus } from '@/graphql/generated/types'
import { formatSize, useAttachments, type AttachmentMeta, type PendingAttachment } from '@/hooks/useAttachments'

export type ComposerMode = 'normal' | 'plan' | 'autonomous'
export type ComposerScope = 'thread' | 'all'
export type AutonomousDuration = '1h' | '4h' | '24h'

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
  agentRetry?: {
    attempt: number
    maxAttempts: number
    error: string | null
    nextDelayMs: number
    final: boolean
  } | null
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

const DRAFT_PREFIX = 'spidey.draft:'

function loadDraft(key?: string): string {
  if (!key) return ''
  try {
    return localStorage.getItem(DRAFT_PREFIX + key) || ''
  } catch {
    return ''
  }
}

function saveDraft(key: string | undefined, value: string) {
  if (!key) return
  try {
    if (value) localStorage.setItem(DRAFT_PREFIX + key, value)
    else localStorage.removeItem(DRAFT_PREFIX + key)
  } catch {
    // storage quota / private mode — drop silently
  }
}

const DURATIONS: AutonomousDuration[] = ['1h', '4h', '24h']

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
  const [value, setValue] = useState<string>(() => loadDraft(draftKey))
  const [durationLocal, setDurationLocal] = useState<AutonomousDuration>('1h')
  const duration = durationProp ?? durationLocal
  const setDuration = setDurationProp ?? setDurationLocal
  const taRef = useRef<HTMLTextAreaElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [dragging, setDragging] = useState(false)
  const dragDepthRef = useRef(0)
  // Working-dirs row state: the chip list is controlled (workingDirs
  // prop), but the add-dir input is a local editing affordance that
  // lives here so it doesn't leak into the caller's state shape.
  const [addingDir, setAddingDir] = useState(false)
  const [dirInput, setDirInput] = useState('')
  const workdirsEnabled = workingDirs !== undefined && onWorkingDirsChange !== undefined
  const sandboxEnabled = sandboxed !== undefined && onSandboxedChange !== undefined

  const attachmentsEnabled = !!threadId
  const { pending, upload, remove, clear, metaForSend } = useAttachments(threadId ?? '')

  const running = agentStatus === AgentStatus.Running
  const paused = agentStatus === AgentStatus.Paused
  const runtimeActive = running || paused

  // Swap draft when the thread (draftKey) changes. Without this, text typed
  // in thread A would appear in thread B after navigation.
  useEffect(() => {
    setValue(loadDraft(draftKey))
  }, [draftKey])

  // Persist on change — NOT via a [draftKey, value] effect. That form fires
  // once with the stale outgoing `value` under the new `draftKey`, clobbering
  // the destination thread's saved draft before the load effect can run.

  useEffect(() => {
    if (taRef.current) {
      taRef.current.style.height = 'auto'
      taRef.current.style.height = Math.min(240, taRef.current.scrollHeight) + 'px'
    }
  }, [value])

  // True iff at least one attachment finished uploading. Text may be
  // empty when the user is shipping pure-attachment prompts ("look at
  // this file"), so the send gate is `text.trim() || haveDoneAttach`.
  const haveDoneAttach = pending.some((x) => x.status === 'done' && x.meta)
  const anyUploading = pending.some((x) => x.status === 'uploading')

  function submit(overrideAutonomous = false) {
    const text = value.trim()
    if (!text && !haveDoneAttach) return
    // Block submission while uploads are still in flight — sending now
    // would drop the pending files from the turn.
    if (anyUploading) return
    const attachments = haveDoneAttach ? metaForSend() : undefined
    // When paused, the submit button is Resume — pressing it sends the
    // typed text as a correction, or resumes with no correction if empty.
    // Paused resume is text-only; attachments are a new-turn concept.
    if (paused && onResumeAgent) {
      setValue('')
      saveDraft(draftKey, '')
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
    saveDraft(draftKey, '')
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
    // Only intercept the paste when there's at least one file — text
    // paste must fall through to the textarea's default handler.
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

  function commitDir() {
    if (!onWorkingDirsChange) return
    const v = dirInput.trim()
    if (v && workingDirs && !workingDirs.includes(v)) {
      onWorkingDirsChange([...workingDirs, v])
    }
    setDirInput('')
    setAddingDir(false)
  }

  function removeDir(d: string) {
    if (!onWorkingDirsChange || !workingDirs) return
    onWorkingDirsChange(workingDirs.filter((x) => x !== d))
  }

  function onKey(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key !== 'Enter' || !(e.metaKey || e.ctrlKey)) return
    e.preventDefault()
    // Stop the window-level shortcut handler from also flipping mode
    // after we've already handled the key here.
    e.stopPropagation()
    // ⌘⇧↵ / Ctrl+Shift+↵ — submit as autonomous regardless of current mode.
    submit(e.shiftKey)
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
            ? `Goal for the autonomous run. ${IS_MAC ? '⌘↵' : 'Ctrl+↵'} to start.`
            : `Continue the thread. ${IS_MAC ? '⌘↵' : 'Ctrl+↵'} to send.`

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
  // a regular chat turn had no way to interrupt. Stop belongs on any
  // turn the user might want to abort; pause stays autonomous-only.
  const statusVisible = runtimeActive || streaming

  const statusLabel = paused
    ? 'paused'
    : agentIsAutonomous
      ? `autonomous${agentElapsed ? ` · ${agentElapsed}` : ''}`
      : running
        ? `running${agentElapsed ? ` · ${agentElapsed}` : ''}`
        : 'working'

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
        {agentRetry && (
          <div
            className="composer-retry"
            data-final={agentRetry.final && agentRetry.error ? 'true' : undefined}
            role="status"
            aria-live="polite"
          >
            <span className="composer-retry-kicker">
              {agentRetry.final && agentRetry.error ? 'retry exhausted' : 'retrying'}
            </span>
            <span className="composer-retry-progress">
              {agentRetry.attempt}/{agentRetry.maxAttempts}
            </span>
            {!agentRetry.final && agentRetry.nextDelayMs > 0 && (
              <RetryCountdown nextDelayMs={agentRetry.nextDelayMs} />
            )}
            {agentRetry.error && (
              <span className="composer-retry-error" title={agentRetry.error}>
                {agentRetry.error.length > 80 ? agentRetry.error.slice(0, 80) + '…' : agentRetry.error}
              </span>
            )}
          </div>
        )}
        {statusVisible && (
          <div className="composer-status" data-state={paused ? 'paused' : running ? 'running' : 'working'}>
            <span className="composer-status-tick" aria-hidden="true" />
            <span className="composer-status-label">{statusLabel}</span>
            <span style={{ flex: 1 }} />
            {running && onPauseAgent && (
              <button
                className="composer-status-btn"
                onClick={onPauseAgent}
                disabled={agentControlsPending}
                title="Pause"
              >
                <IconPause size={10} />
                <span>pause</span>
              </button>
            )}
            {paused && onResumeAgent && !value.trim() && (
              <button
                className="composer-status-btn"
                onClick={() => onResumeAgent(undefined)}
                disabled={agentControlsPending}
                title="Resume (no correction)"
              >
                <IconPlay size={10} />
                <span>resume</span>
              </button>
            )}
            {onStopAgent && (
              <button
                className="composer-status-btn danger"
                onClick={onStopAgent}
                disabled={agentControlsPending}
                title="Stop"
              >
                <IconStop size={10} />
                <span>stop</span>
              </button>
            )}
          </div>
        )}
        {(workdirsEnabled || sandboxEnabled) && (
          <div className="composer-workdirs" role="list">
            {workdirsEnabled && workingDirs!.length === 0 && !addingDir && (
              <span className="composer-workdirs-empty">no dir</span>
            )}
            {workdirsEnabled &&
              workingDirs!.map((d) => (
                <span key={d} className="composer-workdir-chip" role="listitem">
                  <span className="composer-workdir-path">{d}</span>
                  <button
                    type="button"
                    className="composer-workdir-remove"
                    onClick={() => removeDir(d)}
                    aria-label={`Remove ${d}`}
                    title="remove"
                  >
                    <IconX size={10} />
                  </button>
                </span>
              ))}
            {workdirsEnabled &&
              (addingDir ? (
                <input
                  autoFocus
                  className="composer-workdir-input"
                  value={dirInput}
                  onChange={(e) => setDirInput(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault()
                      commitDir()
                    } else if (e.key === 'Escape') {
                      setAddingDir(false)
                      setDirInput('')
                    }
                  }}
                  onBlur={commitDir}
                  placeholder="/path/to/repo"
                />
              ) : (
                <button
                  type="button"
                  className="composer-workdir-add"
                  onClick={() => setAddingDir(true)}
                >
                  <IconPlus size={10} /> mount dir
                </button>
              ))}
            {sandboxEnabled && (
              <span className="composer-sandbox-select" title="Sandbox mode">
                sandbox
                <button
                  type="button"
                  data-on={sandboxed === true}
                  onClick={() => onSandboxedChange!(true)}
                >
                  on
                </button>
                <button
                  type="button"
                  data-on={sandboxed === false}
                  onClick={() => onSandboxedChange!(false)}
                >
                  off
                </button>
              </span>
            )}
          </div>
        )}
        {pending.length > 0 && (
          <div className="composer-attachments" role="list">
            {pending.map((p) => (
              <AttachmentChip key={p.localId} pending={p} onRemove={() => remove(p.localId)} />
            ))}
          </div>
        )}
        <textarea
          ref={taRef}
          className="composer-input"
          placeholder={placeholder}
          value={value}
          onChange={(e) => {
            const v = e.target.value
            setValue(v)
            saveDraft(draftKey, v)
          }}
          onKeyDown={onKey}
          onPaste={onPaste}
          disabled={textareaDisabled}
          rows={2}
        />
        <div className="composer-foot">
          {attachmentsEnabled && (
            <>
              <input
                ref={fileInputRef}
                type="file"
                multiple
                style={{ display: 'none' }}
                onChange={onFileInputChange}
              />
              <button
                type="button"
                className="composer-attach-btn"
                onClick={openFilePicker}
                disabled={runtimeActive || paused}
                title="Attach files"
                aria-label="Attach files"
              >
                <IconPaperclip size={12} />
              </button>
            </>
          )}
          <span className="mode-select">
            <button data-on={mode === 'normal'} onClick={() => setMode('normal')} disabled={runtimeActive}>chat</button>
            <button data-on={mode === 'plan'} onClick={() => setMode('plan')} disabled={runtimeActive}>plan</button>
            <button data-on={mode === 'autonomous'} onClick={() => setMode('autonomous')} disabled={runtimeActive}>autonomous</button>
          </span>
          {mode === 'autonomous' ? (
            <span className="scope-select" title="Autonomous run duration">
              for
              {DURATIONS.map((d) => (
                <button key={d} data-on={duration === d} onClick={() => setDuration(d)} disabled={runtimeActive}>
                  {d}
                </button>
              ))}
            </span>
          ) : (
            <span className="scope-select" title="Selection scope">
              scope
              <button data-on={scope === 'thread'} onClick={() => setScope('thread')} disabled={runtimeActive}>thread</button>
              <button data-on={scope === 'all'} onClick={() => setScope('all')} disabled={runtimeActive}>all</button>
            </span>
          )}
          <span className="spacer" />
          {value.length > 0 && !runtimeActive && (
            <span className="composer-hint">
              {value.length} chars · {IS_MAC ? '⌘↵' : 'Ctrl+↵'}
            </span>
          )}
          <button
            className="composer-send"
            onClick={() => submit()}
            disabled={
              paused
                ? agentControlsPending
                : running || streaming || anyUploading || (!value.trim() && !haveDoneAttach)
            }
            data-mode={paused ? 'resume' : mode}
          >
            {sendLabel}
          </button>
        </div>
      </div>
    </div>
  )
}

// RetryCountdown renders a live wall-clock countdown of remaining
// time until the next retry attempt. The backend sends nextDelayMs
// once, at the moment the retry is scheduled — that number is a
// frozen snapshot. Without a client-side timer the UI would show
// "next in 58s" for the whole wait and never tick. We record the
// arrival time and re-render once per second against the wall clock.
// Resets when `nextDelayMs` changes (a fresh retry event arrived).
function RetryCountdown({ nextDelayMs }: { nextDelayMs: number }) {
  const arrivedRef = useRef(Date.now())
  const [, forceTick] = useState(0)

  // Reset the anchor whenever a new delay arrives.
  useEffect(() => {
    arrivedRef.current = Date.now()
    forceTick((n) => n + 1)
  }, [nextDelayMs])

  // 1 Hz tick — precise enough for human-readable countdown, cheap.
  useEffect(() => {
    const id = window.setInterval(() => forceTick((n) => n + 1), 1000)
    return () => window.clearInterval(id)
  }, [])

  const elapsed = Date.now() - arrivedRef.current
  const remainingMs = Math.max(0, nextDelayMs - elapsed)
  const secs = Math.ceil(remainingMs / 1000)
  return (
    <span className="composer-retry-wait">
      next in {secs}s
    </span>
  )
}

function AttachmentChip({
  pending,
  onRemove,
}: {
  pending: PendingAttachment
  onRemove: () => void
}) {
  const name = pending.meta?.filename ?? pending.file.name
  const size = pending.meta?.sizeBytes ?? pending.file.size
  const title = pending.status === 'error'
    ? pending.error || 'upload failed'
    : `${name} · ${formatSize(size)}`
  return (
    <span
      className="composer-attach-chip"
      data-status={pending.status}
      role="listitem"
      title={title}
    >
      {pending.status === 'uploading' && (
        <span className="composer-attach-spin" aria-hidden="true">
          <IconSpinner size={10} />
        </span>
      )}
      <span className="composer-attach-name">{name}</span>
      <span className="composer-attach-size">{formatSize(size)}</span>
      <button
        type="button"
        className="composer-attach-remove"
        onClick={onRemove}
        aria-label={`Remove ${name}`}
      >
        <IconX size={8} />
      </button>
    </span>
  )
}
