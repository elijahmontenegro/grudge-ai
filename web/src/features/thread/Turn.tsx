import { type ReactNode, useState } from 'react'
import { ToolCall } from './ToolCall'
import { AskQuestionRow } from './AskQuestionRow'
import { MarkdownBody } from '@/features/thread/MarkdownBody'
import { formatSize } from '@/hooks/useAttachments'
import type { ThreadMessage } from '@/hooks/useThreadMessages'

type Attachment = ThreadMessage['attachments'][number]

interface TurnProps {
  prompt: ThreadMessage
  /** Thread id needed for attachment URLs (gql Message doesn't
   *  carry the parent thread id). */
  threadId: string
  /** One user prompt can produce many assistant steps (thinking / tool calls
   *  / final text). Render all of them in order so nothing is dropped. */
  responses: ThreadMessage[]
  /** Shown when the turn is pending and the agent isn't actively streaming. */
  pendingLabel?: string
  last?: boolean
  editable?: boolean
  /** Edit handler lifted from the page — returns the new branched thread id
   *  on success so the caller can navigate. Null on failure. */
  onEdit?: (position: number, newContent: string) => Promise<string | null>
  /** Whether an edit is currently in flight; render state inside the turn. */
  editInFlight?: boolean
  /** Per-tool extras slot. Called for every tool call rendered; returns
   *  interactive content (AskUserQuestion answer form, ExitPlanMode
   *  approval card, etc) to inject into the tool's expanded body. Null
   *  for tools without interactive extras. See CLAUDE.md:
   *  "Tool outputs render in their tool's slot." */
  renderToolExtras?: (
    call: { id: string; name: string; arguments: string; status: string; result: string | null },
  ) => { extras: ReactNode; forceOpen?: boolean } | null
}

export function Turn({
  prompt,
  threadId,
  responses,
  pendingLabel,
  last,
  editable,
  onEdit,
  editInFlight,
  renderToolExtras,
}: TurnProps) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(prompt.content)

  async function save() {
    if (!draft.trim() || draft.trim() === prompt.content) {
      setEditing(false)
      return
    }
    if (onEdit) {
      await onEdit(prompt.position, draft.trim())
    }
    setEditing(false)
  }

  return (
    <section className={`turn ${last ? 'turn-last' : ''}`}>
      <div className="turn-body">
        {editing ? (
          <div style={{ marginBottom: 16 }}>
            <textarea
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Escape') {
                  setEditing(false)
                  setDraft(prompt.content)
                } else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault()
                  void save()
                }
              }}
              rows={Math.max(2, draft.split('\n').length)}
              autoFocus
              style={{
                width: '100%',
                padding: '10px 12px',
                fontFamily: 'var(--serif)',
                fontSize: 18,
                lineHeight: 1.5,
                color: 'var(--ink)',
                background: 'var(--paper-2)',
                border: '1px solid var(--rule)',
                borderRadius: 3,
                resize: 'vertical',
              }}
            />
            <div
              style={{
                display: 'flex',
                gap: 8,
                marginTop: 8,
                fontFamily: 'var(--mono)',
                fontSize: 11,
                alignItems: 'center',
              }}
            >
              <button
                className="plan-approve"
                onClick={() => void save()}
                disabled={editInFlight}
              >
                {editInFlight ? 'branching…' : 'save & branch'}
              </button>
              <button
                className="plan-reject"
                onClick={() => {
                  setEditing(false)
                  setDraft(prompt.content)
                }}
              >
                cancel
              </button>
              <span style={{ color: 'var(--muted-2)', marginLeft: 'auto' }}>
                editing forks a new thread at #{prompt.position}
              </span>
            </div>
          </div>
        ) : (
          <div className="turn-prompt" style={{ position: 'relative' }}>
            {prompt.content}
            {prompt.attachments && prompt.attachments.length > 0 && (
              <TurnAttachments
                threadId={threadId}
                attachments={prompt.attachments}
              />
            )}
            {editable && onEdit && (
              <button
                className="turn-prompt-edit"
                onClick={() => {
                  setDraft(prompt.content)
                  setEditing(true)
                }}
                title="edit — forks a new thread from this turn"
              >
                edit
              </button>
            )}
          </div>
        )}

        <div className="turn-response">
          {responses.length === 0 && pendingLabel && (
            <p style={{ color: 'var(--muted)', fontStyle: 'italic' }}>{pendingLabel}</p>
          )}
          <TurnResponses responses={responses} renderToolExtras={renderToolExtras} />
        </div>
      </div>
    </section>
  )
}

function TurnResponses({
  responses,
  renderToolExtras,
}: {
  responses: ThreadMessage[]
  renderToolExtras?: TurnProps['renderToolExtras']
}) {
  // Index every tool result across the turn. The agent commonly
  // emits a tool call in one message and its result in the next,
  // so consumers want a single map keyed by callId.
  const resultById: Record<string, { result: string; status: 'ok' | 'running' | 'error' }> = {}
  for (const r of responses) {
    for (const tr of r.toolResults) {
      if (resultById[tr.toolCallId]) continue
      const looksError =
        /^\s*(error|failed|exception|traceback)/i.test(tr.content) ||
        /\b(connection refused|timed? out|not found|permission denied|actively refused)\b/i.test(
          tr.content,
        )
      resultById[tr.toolCallId] = {
        result: tr.content,
        status: looksError ? 'error' : 'ok',
      }
    }
  }

  const seenCallIds = new Set<string>()
  const out: React.ReactNode[] = []

  responses.forEach((r, idx) => {
    const hasThinking = !!r.thinking
    const newCalls = r.toolCalls.filter((t) => !seenCallIds.has(t.id))
    newCalls.forEach((t) => seenCallIds.add(t.id))
    const hasText = !!r.content

    if (!hasThinking && newCalls.length === 0 && !hasText) return

    if (hasThinking) out.push(<div key={`${r.id}-think`} className="thinking">{r.thinking}</div>)
    newCalls.forEach((t) => {
      const paired = resultById[t.id]
      const effectiveResult = paired?.result ?? ''
      const effectiveStatus = paired?.status ?? 'running'
      // AskUserQuestion gets a specialized compact row — the generic
      // ToolCall presentation (name + JSON args + status) reads badly
      // when the "output" is a conversational answer we already
      // surface via the composer-takeover AnswerStage.
      if (t.name === 'AskUserQuestion') {
        out.push(
          <AskQuestionRow
            key={`${r.id}-tool-${t.id}`}
            args={t.arguments}
            result={effectiveResult}
            status={effectiveStatus}
            pending={effectiveStatus === 'running' && !effectiveResult}
          />,
        )
        return
      }
      const extrasResult = renderToolExtras
        ? renderToolExtras({
            id: t.id,
            name: t.name,
            arguments: t.arguments,
            status: effectiveStatus,
            result: effectiveResult || null,
          })
        : null
      out.push(
        <ToolCall
          key={`${r.id}-tool-${t.id}`}
          t={{
            name: t.name,
            args: t.arguments,
            result: effectiveResult,
            status: effectiveStatus,
          }}
          extras={extrasResult?.extras}
          forceOpen={extrasResult?.forceOpen}
        />,
      )
    })
    if (hasText) out.push(<MarkdownBody key={`${r.id}-text-${idx}`} text={r.content} />)
  })

  return <>{out}</>
}

function TurnAttachments({
  threadId,
  attachments,
}: {
  threadId: string
  attachments: Attachment[]
}) {
  return (
    <div className="turn-attachments">
      {attachments.map((a) => {
        const url = `/api/attachments/${encodeURIComponent(threadId)}/${encodeURIComponent(a.id)}/${encodeURIComponent(a.filename)}`
        if (a.mimeType.startsWith('image/')) {
          return (
            <a
              key={a.id}
              className="turn-attach turn-attach-image"
              href={url}
              target="_blank"
              rel="noopener noreferrer"
              title={`${a.filename} · ${formatSize(a.sizeBytes)}`}
            >
              <img src={url} alt={a.filename} loading="lazy" />
              <span className="turn-attach-caption">{a.filename}</span>
            </a>
          )
        }
        return (
          <a
            key={a.id}
            className="turn-attach turn-attach-file"
            href={url}
            target="_blank"
            rel="noopener noreferrer"
            title={a.path}
          >
            <span className="turn-attach-name">{a.filename}</span>
            <span className="turn-attach-size">{formatSize(a.sizeBytes)}</span>
          </a>
        )
      })}
    </div>
  )
}
