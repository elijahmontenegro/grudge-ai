import { useEffect, useState } from 'react'
import type { PendingToolCall } from '@/hooks/useToolExecutions'

interface ToolApprovalPromptProps {
  pending: PendingToolCall[]
  busy: boolean
  onApprove: (callId: string) => void
  onDeny: (callId: string, reason?: string) => void
}

const APPROVAL_TIMEOUT_MS = 5 * 60 * 1000 // backend uses 5 min

export function ToolApprovalPrompt({
  pending,
  busy,
  onApprove,
  onDeny,
}: ToolApprovalPromptProps) {
  const [now, setNow] = useState(() => Date.now())
  // Per-call UI state: which call is in "provide deny reason" mode, and the
  // in-progress reason text keyed by callId so it survives re-renders when
  // another pending tool's timer ticks.
  const [denyingId, setDenyingId] = useState<string | null>(null)
  const [reasons, setReasons] = useState<Record<string, string>>({})

  useEffect(() => {
    if (pending.length === 0) return
    const iv = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(iv)
  }, [pending.length])

  // Clear deny-mode if the targeted call was removed from pending (approved
  // by another tab, timed out, etc).
  useEffect(() => {
    if (denyingId && !pending.some((p) => p.callId === denyingId)) {
      setDenyingId(null)
    }
  }, [pending, denyingId])

  if (pending.length === 0) return null

  function submitDeny(callId: string) {
    const reason = (reasons[callId] || '').trim()
    onDeny(callId, reason || undefined)
    setDenyingId(null)
    setReasons((r) => {
      const next = { ...r }
      delete next[callId]
      return next
    })
  }

  return (
    <div
      className="plan-block"
      data-decision={null}
      style={{
        margin: '8px 40px 0',
        maxWidth: 820,
        borderLeftColor: 'var(--danger)',
      }}
    >
      <header className="plan-head">
        <span className="plan-kind" style={{ color: 'var(--danger)' }}>
          approval required · {pending.length} pending
        </span>
        <span className="plan-rationale" style={{ fontStyle: 'normal' }}>
          The agent wants to run a gated tool. Approve to continue, deny to block and surface the
          reason back to the agent.
        </span>
      </header>
      <div style={{ padding: '4px 0' }}>
        {pending.map((p) => {
          const elapsed = now - p.arrivedAt
          const remaining = Math.max(0, APPROVAL_TIMEOUT_MS - elapsed)
          const mins = Math.floor(remaining / 60000)
          const secs = Math.floor((remaining % 60000) / 1000)
          const isDenying = denyingId === p.callId
          return (
            <div
              key={p.callId}
              style={{
                padding: '10px 14px',
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
                    fontSize: 12,
                    color: 'var(--ink)',
                    marginBottom: 4,
                  }}
                >
                  {p.toolName}
                </div>
                <pre
                  style={{
                    fontFamily: 'var(--mono)',
                    fontSize: 11,
                    color: 'var(--muted)',
                    margin: 0,
                    whiteSpace: 'pre-wrap',
                    wordBreak: 'break-word',
                  }}
                >
                  {p.arguments}
                </pre>
                <div
                  style={{
                    fontFamily: 'var(--mono)',
                    fontSize: 10,
                    color: 'var(--muted-2)',
                    marginTop: 4,
                    letterSpacing: '0.04em',
                  }}
                >
                  expires in {mins}:{String(secs).padStart(2, '0')}
                </div>
                {isDenying && (
                  <div style={{ marginTop: 8 }}>
                    <textarea
                      autoFocus
                      value={reasons[p.callId] ?? ''}
                      onChange={(e) =>
                        setReasons((r) => ({ ...r, [p.callId]: e.target.value }))
                      }
                      onKeyDown={(e) => {
                        if (e.key === 'Escape') {
                          setDenyingId(null)
                        } else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                          e.preventDefault()
                          submitDeny(p.callId)
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
                  </div>
                )}
              </div>
              <div style={{ display: 'flex', gap: 6, alignItems: 'start' }}>
                {isDenying ? (
                  <>
                    <button
                      className="plan-reject"
                      onClick={() => submitDeny(p.callId)}
                      disabled={busy}
                    >
                      send deny
                    </button>
                    <button
                      className="plan-approve"
                      style={{ background: 'var(--muted)' }}
                      onClick={() => setDenyingId(null)}
                      disabled={busy}
                    >
                      cancel
                    </button>
                  </>
                ) : (
                  <>
                    <button
                      className="plan-approve"
                      onClick={() => onApprove(p.callId)}
                      disabled={busy}
                    >
                      approve
                    </button>
                    <button
                      className="plan-reject"
                      onClick={() => setDenyingId(p.callId)}
                      disabled={busy}
                    >
                      deny
                    </button>
                  </>
                )}
              </div>
            </div>
          )
        })}
      </div>
    </div>
  )
}
