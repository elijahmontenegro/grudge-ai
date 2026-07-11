import type { StreamState } from '@/hooks/useSendAndStream'
import type { LiveSubagent } from '@/hooks/useSubagentProgress'
import type { LiveToolCall, PendingToolCall } from '@/state/toolExecutions'
import { ToolCall } from '@/features/thread/ToolCall'
import { ToolApprovalSlot } from '@/features/thread/ToolApprovalSlot'

interface StreamingTurnProps {
  stream: StreamState
  subagents?: LiveSubagent[]
  liveTools?: LiveToolCall[]
  // Tool approvals (e.g. Bash) fire mid-stream, before the call is persisted
  // into the corpus — so the approve/deny surface must live here, not only on
  // the completed <Turn>. Without it the call hangs at "pending" (a spinner)
  // and the only exit is Stop.
  pendingApprovals?: PendingToolCall[]
  onApproveTool?: (callId: string) => void
  onDenyTool?: (callId: string, reason?: string) => void
  approvalsBusy?: boolean
}

export function StreamingTurn({
  stream,
  subagents = [],
  liveTools = [],
  pendingApprovals = [],
  onApproveTool,
  onDenyTool,
  approvalsBusy,
}: StreamingTurnProps) {
  const pendingApprovalByCallId = new Map<string, PendingToolCall>()
  for (const p of pendingApprovals) pendingApprovalByCallId.set(p.callId, p)
  // ToolExecution subscription is the only source of tool calls during
  // streaming — they don't appear on the StreamEvent (ADK emits
  // FunctionCall as a discrete event, not a streamable delta, so a
  // marker on the stream would carry no information the live list
  // doesn't already have).
  const anyToolsActive = liveTools.some(
    (t) => t.status === 'running' || t.status === 'pending',
  )

  const lastItem = stream.items[stream.items.length - 1]
  const stageLabel = !stream.messageId
    ? 'rrc · scoring prerequisites'
    : lastItem?.kind === 'thinking'
      ? 'agent · thinking'
      : anyToolsActive && lastItem?.kind !== 'text'
        ? 'agent · using tools'
        : lastItem?.kind === 'text'
          ? 'agent · responding'
          : 'rrc · restoring messages'

  return (
    <section className="turn turn-streaming">
      <div className="turn-body">
        <div className="streaming-stage">
          {stageLabel}
          <span className="dot-trail">
            <i /><i /><i />
          </span>
        </div>
        <div className="turn-response">
          {stream.items.map((it, i) => {
            if (it.kind === 'thinking') {
              return (
                <div key={`think-${i}`} className="thinking">
                  {it.text}
                </div>
              )
            }
            return (
              <p
                key={`text-${i}`}
                style={{ whiteSpace: 'pre-wrap', wordWrap: 'break-word', margin: '0 0 12px' }}
              >
                {/* Raw text during streaming — markdown reparse per
                    delta was O(n²) and half-closed mid-stream
                    constructs flashed broken layouts. Turn.tsx
                    renders markdown once the stream settles. */}
                {it.text}
                {i === stream.items.length - 1 && <span className="caret" />}
              </p>
            )
          })}
          {liveTools.map((t) => {
            const status =
              t.status === 'completed' && !t.isError
                ? 'ok'
                : t.isError || t.status === 'error'
                  ? 'error'
                  : 'running'
            const pendingApproval = pendingApprovalByCallId.get(t.callId)
            const extras =
              pendingApproval && onApproveTool && onDenyTool ? (
                <ToolApprovalSlot
                  pending={pendingApproval}
                  busy={!!approvalsBusy}
                  onApprove={onApproveTool}
                  onDeny={onDenyTool}
                />
              ) : undefined
            return (
              <ToolCall
                key={`tool-${t.callId}`}
                t={{
                  name: t.toolName,
                  args: t.arguments,
                  result: t.result ?? '',
                  status,
                }}
                extras={extras}
                forceOpen={!!pendingApproval}
              />
            )
          })}
          {subagents.map((s) => (
            <div key={s.forkThreadId} className="subagent">
              <div className="subagent-head">
                <span className="subagent-task">{s.task}</span>
                <span className="subagent-meta">
                  {s.status} · {s.roundCount} rounds
                </span>
              </div>
            </div>
          ))}
          {stream.error && (
            <p style={{ color: 'var(--danger)', fontFamily: 'var(--mono)', fontSize: 12 }}>
              stream error · {stream.error}
            </p>
          )}
        </div>
      </div>
    </section>
  )
}
