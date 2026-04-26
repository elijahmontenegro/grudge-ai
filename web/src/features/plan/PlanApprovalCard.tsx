import { useEffect, useRef, useState } from 'react'

interface PlanApprovalCardProps {
  planContent: string
  approving: boolean
  rejecting: boolean
  editing: boolean
  onApprove: (autonomous: boolean) => void
  onReject: (feedback: string) => void
  onEdit: (content: string) => Promise<void> | void
  /** Ask the App shell to open the artifacts panel so the user can read
   *  the plan body. The card deliberately doesn't re-render the plan —
   *  that job belongs to the viewer panel. */
  onViewPlan: () => void
}

type Mode = 'idle' | 'revising' | 'editing'

export function PlanApprovalCard({
  planContent,
  approving,
  rejecting,
  editing,
  onApprove,
  onReject,
  onEdit,
  onViewPlan,
}: PlanApprovalCardProps) {
  const [mode, setMode] = useState<Mode>('idle')
  const [feedback, setFeedback] = useState('')
  const [draft, setDraft] = useState(planContent)
  const feedbackRef = useRef<HTMLTextAreaElement>(null)
  const editorRef = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    if (mode !== 'editing') setDraft(planContent)
  }, [planContent, mode])

  useEffect(() => {
    if (mode === 'revising') feedbackRef.current?.focus()
    if (mode === 'editing') editorRef.current?.focus()
  }, [mode])

  const busy = approving || rejecting || editing

  function sendRevision() {
    const text = feedback.trim()
    if (!text) return
    onReject(text)
    setMode('idle')
    setFeedback('')
  }

  async function saveEdit() {
    if (draft === planContent) {
      setMode('idle')
      return
    }
    await onEdit(draft)
    setMode('idle')
  }

  return (
    <section className="plan-card" aria-label="Plan approval">
      <header className="plan-card-head">
        <span className="plan-card-eyebrow">plan ready</span>
        <button className="plan-card-link" onClick={onViewPlan} type="button">
          view plan.adoc →
        </button>
      </header>

      {mode === 'idle' && (
        <>
          <p className="plan-card-lede">
            The agent wrote a plan and is waiting on you. Read it in the artifacts panel, then
            choose.
          </p>
          <div className="plan-card-actions">
            <button
              className="plan-approve"
              type="button"
              disabled={busy}
              onClick={() => onApprove(false)}
            >
              {approving ? 'approving…' : 'approve & execute'}
            </button>
            <button
              className="plan-approve plan-approve-auto"
              type="button"
              disabled={busy}
              onClick={() => onApprove(true)}
            >
              approve · autonomous
            </button>
            <button
              className="plan-reject"
              type="button"
              disabled={busy}
              onClick={() => setMode('revising')}
            >
              request revision
            </button>
            <button
              className="plan-reject"
              type="button"
              disabled={busy}
              onClick={() => {
                setDraft(planContent)
                setMode('editing')
              }}
              title="Edit plan.adoc directly"
            >
              edit source
            </button>
          </div>
        </>
      )}

      {mode === 'revising' && (
        <>
          <label className="plan-card-label">What should change?</label>
          <textarea
            ref={feedbackRef}
            className="plan-card-textarea"
            rows={3}
            value={feedback}
            onChange={(e) => setFeedback(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Escape') {
                setMode('idle')
                setFeedback('')
              } else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                e.preventDefault()
                sendRevision()
              }
            }}
            placeholder="Describe what to revise. The agent will rewrite plan.adoc."
          />
          <div className="plan-card-actions">
            <button
              className="plan-approve"
              type="button"
              disabled={busy || !feedback.trim()}
              onClick={sendRevision}
            >
              {rejecting ? 'revising…' : 'send revision'}
            </button>
            <button
              className="plan-reject"
              type="button"
              disabled={busy}
              onClick={() => {
                setMode('idle')
                setFeedback('')
              }}
            >
              cancel
            </button>
          </div>
        </>
      )}

      {mode === 'editing' && (
        <>
          <label className="plan-card-label">Edit plan.adoc</label>
          <textarea
            ref={editorRef}
            className="plan-card-textarea plan-card-textarea-code"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Escape') {
                setMode('idle')
                setDraft(planContent)
              } else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                e.preventDefault()
                void saveEdit()
              }
            }}
            spellCheck={false}
          />
          <div className="plan-card-actions">
            <button
              className="plan-approve"
              type="button"
              disabled={editing || draft === planContent}
              onClick={() => void saveEdit()}
            >
              {editing ? 'saving…' : 'save edits'}
            </button>
            <button
              className="plan-reject"
              type="button"
              disabled={editing}
              onClick={() => {
                setMode('idle')
                setDraft(planContent)
              }}
            >
              cancel
            </button>
          </div>
        </>
      )}
    </section>
  )
}
