import type { StreamState } from '@/hooks/useSendAndStream'
import type { LiveSubagent } from '@/hooks/useSubagentProgress'
import type { LiveToolCall } from '@/hooks/useToolExecutions'
import { ToolCall } from '@/components/molecules/ToolCall'

interface StreamingTurnProps {
  stream: StreamState
  subagents?: LiveSubagent[]
  liveTools?: LiveToolCall[]
}

export function StreamingTurn({ stream, subagents = [], liveTools = [] }: StreamingTurnProps) {
  // ToolExecution subscription drives status transitions; match items to
  // that live list by id so statuses (running → ok/error) light up as
  // the backend reports them.
  const liveById = new Map(liveTools.map((t) => [t.callId, t]))

  const anyToolsActive =
    liveTools.length > 0
      ? liveTools.some((t) => t.status === 'running' || t.status === 'pending')
      : stream.items.some((it) => it.kind === 'toolCall')

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
            if (it.kind === 'text') {
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
            }
            const live = liveById.get(it.id)
            const status =
              live?.status === 'completed' && !live?.isError
                ? 'ok'
                : live?.isError || live?.status === 'error'
                  ? 'error'
                  : 'running'
            return (
              <ToolCall
                key={`tool-${it.id}`}
                t={{
                  name: it.name,
                  args: it.arguments,
                  result: live?.result ?? '',
                  status,
                }}
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
