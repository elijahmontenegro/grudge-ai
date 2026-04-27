import { useCallback, useEffect, useRef, useState } from 'react'
import { useMutation } from '@apollo/client/react'
import { UPDATE_THREAD } from '@/graphql/operations'
import { IconPlus, IconSettings, IconX } from '@/primitives/icons'
import { IconButton } from '@/primitives/IconButton'
import type {
  UpdateThreadMutation,
  UpdateThreadMutationVariables,
} from '@/graphql/generated/types'
import { usePopover } from '@/primitives/usePopover'

interface ThreadConfigPopoverProps {
  threadId: string
  workingDirs: string[]
  sandboxed: boolean
}

/**
 * Unobtrusive gear next to the thread title. Opens a small popover with
 * working-dirs list (add/remove) and sandboxed toggle. Writes fire UPDATE_THREAD
 * and refetch thread messages so the header and future tool calls see fresh config.
 */
export function ThreadConfigPopover({ threadId, workingDirs, sandboxed }: ThreadConfigPopoverProps) {
  const [open, setOpen] = useState(false)
  const [dirs, setDirs] = useState<string[]>(workingDirs)
  const [sbx, setSbx] = useState<boolean>(sandboxed)
  const [newDir, setNewDir] = useState('')
  const anchorRef = useRef<HTMLButtonElement>(null)

  const [updateMut, updateRes] = useMutation<UpdateThreadMutation, UpdateThreadMutationVariables>(
    UPDATE_THREAD,
    // Name-based refetch also updates any ListThreads observers (sidebar +
    // Home) that rendered this thread's stale name/workingDirs/sandboxed.
    { refetchQueries: ['GetThreadMessages', 'ListThreads'] },
  )

  // Sync local editable state with props when the thread (or its loaded
  // config) changes. Deriving on every render would prevent the user from
  // locally mutating before committing.
  useEffect(() => {
    setDirs(workingDirs)
    setSbx(sandboxed)
  }, [workingDirs, sandboxed])

  // Anchor the popover below the gear, switching to right-edge
  // anchoring whenever left-anchoring would overflow the viewport.
  const computePos = useCallback((r: DOMRect) => {
    const width = 360 // approx — must exceed minWidth to avoid under-estimating overflow
    const overflowRight = r.left + width > window.innerWidth - 8
    if (overflowRight) {
      return { top: r.bottom + 6, right: Math.max(8, window.innerWidth - r.right) }
    }
    return { top: r.bottom + 6, left: r.left }
  }, [])
  const handleClose = useCallback(() => setOpen(false), [])
  const { popRef, pos } = usePopover(open, anchorRef, handleClose, computePos)

  async function save(nextDirs: string[], nextSbx: boolean) {
    await updateMut({
      variables: {
        id: threadId,
        name: null,
        workingDirs: nextDirs,
        sandboxed: nextSbx,
      },
    })
  }

  function addDir() {
    const v = newDir.trim()
    if (!v) return
    const next = [...dirs, v]
    setDirs(next)
    setNewDir('')
    void save(next, sbx)
  }

  function removeDir(d: string) {
    const next = dirs.filter((x) => x !== d)
    setDirs(next)
    void save(next, sbx)
  }

  function toggleSbx() {
    const next = !sbx
    setSbx(next)
    void save(dirs, next)
  }

  return (
    <>
      <IconButton
        ref={anchorRef}
        icon={<IconSettings size={18} />}
        label="thread config"
        onClick={() => setOpen((o) => !o)}
        style={{ marginLeft: 10 }}
      />
      {open && pos && (
        <div
          ref={popRef}
          className="user-menu"
          style={{ position: 'fixed', ...pos, minWidth: 320, padding: '10px 14px' }}
        >
          <div
            style={{
              fontFamily: 'var(--mono)',
              fontSize: 10,
              letterSpacing: '0.08em',
              textTransform: 'uppercase',
              color: 'var(--muted)',
              marginBottom: 8,
            }}
          >
            thread config
          </div>
          <div
            style={{
              fontFamily: 'var(--mono)',
              fontSize: 11,
              color: 'var(--muted-2)',
              marginBottom: 6,
            }}
          >
            working directories
          </div>
          <div style={{ display: 'grid', gap: 4, marginBottom: 10 }}>
            {dirs.length === 0 && (
              <div style={{ fontSize: 11, color: 'var(--muted)' }}>
                no directories — agent runs with no filesystem scope
              </div>
            )}
            {dirs.map((d) => (
              <div
                key={d}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 8,
                  padding: '4px 6px',
                  background: 'var(--paper-2)',
                  border: '1px solid var(--rule)',
                  borderRadius: 3,
                  fontFamily: 'var(--mono)',
                  fontSize: 11,
                }}
              >
                <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis' }}>{d}</span>
                <button
                  onClick={() => removeDir(d)}
                  style={{ color: 'var(--muted)' }}
                  title="remove"
                >
                  <IconX size={10} />
                </button>
              </div>
            ))}
          </div>
          <div style={{ display: 'flex', gap: 6, marginBottom: 12 }}>
            <input
              value={newDir}
              onChange={(e) => setNewDir(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  addDir()
                }
              }}
              placeholder="/path/to/repo"
              style={{
                flex: 1,
                padding: '4px 8px',
                fontFamily: 'var(--mono)',
                fontSize: 11,
                border: '1px solid var(--rule)',
                borderRadius: 3,
                background: 'var(--paper)',
              }}
            />
            <IconButton
              icon={<IconPlus size={18} />}
              label="add"
              onClick={addDir}
            />
          </div>
          <label
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              fontFamily: 'var(--mono)',
              fontSize: 11,
              cursor: 'pointer',
              paddingTop: 8,
              borderTop: '1px solid var(--rule)',
            }}
          >
            <input type="checkbox" checked={sbx} onChange={toggleSbx} />
            <span>sandboxed · run shell commands in an isolated sandbox</span>
          </label>
          {updateRes.error && (
            <div
              style={{
                color: 'var(--danger)',
                fontFamily: 'var(--mono)',
                fontSize: 11,
                marginTop: 6,
              }}
            >
              {updateRes.error.message}
            </div>
          )}
        </div>
      )}
    </>
  )
}
