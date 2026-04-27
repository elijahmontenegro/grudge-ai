import { useEffect, useState } from 'react'
import type { PendingToolCall } from '@/state/toolExecutions'

interface ToolApprovalSlotProps {
  pending: PendingToolCall
  busy: boolean
  onApprove: (callId: string) => void
  onDeny: (callId: string, reason?: string) => void
}

const APPROVAL_TIMEOUT_MS = 5 * 60 * 1000 // backend uses 5 min

/**
 * Per-call approval UI rendered inside its tool's slot via the
 * `renderToolExtras` plumbing on Turn. Pure action surface — no card
 * chrome, no "N pending" header — because the surrounding tool-call
 * collapsible already carries the tool name and arguments.
 *
 * Per CLAUDE.md: tool outputs render in their tool's slot. The
 * pending-approval gate is the same shape: it lives next to the call
 * it's gating, not floating elsewhere on the page.
 */
export function ToolApprovalSlot({
  pending,
  busy,
  onApprove,
  onDeny,
}: ToolApprovalSlotProps) {
  const [now, setNow] = useState(() => Date.now())
  const [denying, setDenying] = useState(false)
  const [reason, setReason] = useState('')

  useEffect(() => {
    const iv = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(iv)
  }, [])

  const elapsed = now - pending.arrivedAt
  const remaining = Math.max(0, APPROVAL_TIMEOUT_MS - elapsed)
  const mins = Math.floor(remaining / 60000)
  const secs = Math.floor((remaining % 60000) / 1000)

  function submitDeny() {
    onDeny(pending.callId, reason.trim() || undefined)
    setDenying(false)
    setReason('')
  }

  return (
    <div
      style={{
        padding: '10px 12px',
        borderTop: '1px dashed var(--rule-2)',
        display: 'grid',
        gridTemplateColumns: '1fr auto',
        gap: 14,
        alignItems: 'start',
      }}
    >
      <div>
        <div
          style={{
            fontFamily: 'var(--mono)',
            fontSize: 11,
            color: 'var(--danger)',
            letterSpacing: '0.04em',
            marginBottom: 4,
          }}
        >
          approval required · expires in {mins}:{String(secs).padStart(2, '0')}
        </div>
        {denying && (
          <textarea
            autoFocus
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Escape') setDenying(false)
              else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                e.preventDefault()
                submitDeny()
              }
            }}
            placeholder="Why deny? (surfaced back to the agent)"
            rows={2}
            style={{
              width: '100%',
              padding: '6px 8px',
              fontFamily: 'var(--mono)',
              fontSize: 11,
              color: 'var(--ink)',
              background: 'var(--paper-2)',
              border: '1px solid var(--rule)',
              borderRadius: 3,
              resize: 'vertical',
            }}
          />
        )}
      </div>
      <div style={{ display: 'flex', gap: 6, alignItems: 'start' }}>
        {denying ? (
          <>
            <button className="plan-reject" onClick={submitDeny} disabled={busy}>
              send deny
            </button>
            <button
              className="plan-approve"
              style={{ background: 'var(--muted)' }}
              onClick={() => setDenying(false)}
              disabled={busy}
            >
              cancel
            </button>
          </>
        ) : (
          <>
            <button
              className="plan-approve"
              onClick={() => onApprove(pending.callId)}
              disabled={busy}
            >
              approve
            </button>
            <button className="plan-reject" onClick={() => setDenying(true)} disabled={busy}>
              deny
            </button>
          </>
        )}
      </div>
    </div>
  )
}
