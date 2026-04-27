import { useState } from 'react'
import { IconPlus, IconX } from '@/primitives/icons'

interface WorkdirsProps {
  workingDirs?: string[]
  onWorkingDirsChange?: (dirs: string[]) => void
  sandboxed?: boolean
  onSandboxedChange?: (v: boolean) => void
}

/** Pre-creation working-dirs chip list + sandbox toggle. Shown only
 *  when caller passes the props (Home composer for new threads).
 *  Established threads use ThreadConfigPopover instead. */
export function Workdirs({
  workingDirs,
  onWorkingDirsChange,
  sandboxed,
  onSandboxedChange,
}: WorkdirsProps) {
  const [addingDir, setAddingDir] = useState(false)
  const [dirInput, setDirInput] = useState('')

  const workdirsEnabled = workingDirs !== undefined && onWorkingDirsChange !== undefined
  const sandboxEnabled = sandboxed !== undefined && onSandboxedChange !== undefined
  if (!workdirsEnabled && !sandboxEnabled) return null

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

  return (
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
  )
}
