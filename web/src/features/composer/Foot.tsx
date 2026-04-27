import { type RefObject } from 'react'
import { IconPaperclip } from '@/primitives/icons'
import { IS_MAC } from '@/primitives/platform'
import {
  DURATIONS,
  type AutonomousDuration,
  type ComposerMode,
  type ComposerScope,
} from './types'

interface FootProps {
  attachmentsEnabled: boolean
  fileInputRef: RefObject<HTMLInputElement | null>
  onFileInputChange: (e: React.ChangeEvent<HTMLInputElement>) => void
  openFilePicker: () => void
  runtimeActive: boolean
  paused: boolean
  mode: ComposerMode
  setMode: (m: ComposerMode) => void
  scope: ComposerScope
  setScope: (s: ComposerScope) => void
  duration: AutonomousDuration
  setDuration: (d: AutonomousDuration) => void
  valueLength: number
  sendLabel: string
  sendDisabled: boolean
  onSubmit: () => void
}

/** The composer's footer bar — attach button, mode/scope/duration
 *  selectors, char hint, and the send button. Stateless presenter:
 *  the shell owns submit / mode / scope and passes them in. */
export function Foot({
  attachmentsEnabled,
  fileInputRef,
  onFileInputChange,
  openFilePicker,
  runtimeActive,
  paused,
  mode,
  setMode,
  scope,
  setScope,
  duration,
  setDuration,
  valueLength,
  sendLabel,
  sendDisabled,
  onSubmit,
}: FootProps) {
  return (
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
        <button data-on={mode === 'normal'} onClick={() => setMode('normal')} disabled={runtimeActive}>
          chat
        </button>
        <button data-on={mode === 'plan'} onClick={() => setMode('plan')} disabled={runtimeActive}>
          plan
        </button>
        <button
          data-on={mode === 'autonomous'}
          onClick={() => setMode('autonomous')}
          disabled={runtimeActive}
        >
          autonomous
        </button>
      </span>
      {mode === 'autonomous' ? (
        <span className="scope-select" title="Autonomous run duration">
          for
          {DURATIONS.map((d) => (
            <button
              key={d}
              data-on={duration === d}
              onClick={() => setDuration(d)}
              disabled={runtimeActive}
            >
              {d}
            </button>
          ))}
        </span>
      ) : (
        <span className="scope-select" title="Selection scope">
          scope
          <button data-on={scope === 'thread'} onClick={() => setScope('thread')} disabled={runtimeActive}>
            thread
          </button>
          <button data-on={scope === 'all'} onClick={() => setScope('all')} disabled={runtimeActive}>
            all
          </button>
        </span>
      )}
      <span className="spacer" />
      {valueLength > 0 && !runtimeActive && (
        <span className="composer-hint">
          {valueLength} chars · {IS_MAC ? '⌘↵' : 'Ctrl+↵'}
        </span>
      )}
      <button
        className="composer-send"
        onClick={onSubmit}
        disabled={sendDisabled}
        data-mode={paused ? 'resume' : mode}
      >
        {sendLabel}
      </button>
    </div>
  )
}
