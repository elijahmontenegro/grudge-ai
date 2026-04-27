import { type RefObject } from 'react'
import { IconPaperclip } from '@/primitives/icons'
import { IS_MAC } from '@/primitives/platform'
import { SegButton } from '@/primitives/SegButton'
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
      <SegButton
        className="mode-select"
        value={mode}
        onChange={setMode}
        options={[
          { value: 'normal', label: 'chat', disabled: runtimeActive },
          { value: 'plan', label: 'plan', disabled: runtimeActive },
          { value: 'autonomous', label: 'autonomous', disabled: runtimeActive },
        ]}
      />
      {mode === 'autonomous' ? (
        <SegButton
          className="scope-select"
          title="Autonomous run duration"
          label="for"
          value={duration}
          onChange={setDuration}
          options={DURATIONS.map((d) => ({ value: d, label: d, disabled: runtimeActive }))}
        />
      ) : (
        <SegButton
          className="scope-select"
          title="Selection scope"
          label="scope"
          value={scope}
          onChange={setScope}
          options={[
            { value: 'thread', label: 'thread', disabled: runtimeActive },
            { value: 'all', label: 'all', disabled: runtimeActive },
          ]}
        />
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
