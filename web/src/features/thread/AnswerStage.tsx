import { useEffect, useMemo, useRef, useState } from 'react'
import { IS_MAC } from '@/primitives/platform'
import { IconX } from '@/primitives/icons'
import type { PendingQuestion } from '@/hooks/useToolExecutions'

const OTHER_VALUE = '__other__'

interface AnswerStageProps {
  /** The one question-call currently being answered. Drives the whole
   *  stage. Questions from other calls queue behind it — once this one
   *  resolves the stage re-renders on the next. */
  question: PendingQuestion
  busy: boolean
  /** Submit the user's answers. Returns the underlying mutation
   *  Promise so the stage can await it and surface failures inline —
   *  a stale callId or timeout would otherwise silently hide behind a
   *  blank composer. */
  onAnswer: (
    callId: string,
    answer: string | Record<string, string>,
  ) => void | Promise<void>
  /** Dismiss the stage without sending any answer. Used when the
   *  question is stale (agent restart, timeout) so the composer can
   *  return to normal. */
  onDismiss: (callId: string) => void
}

interface SingleAnswerDraft {
  /** Selected option labels, or `__other__` when the user wants free text. */
  selected: string[]
  /** Free-text input — used when no `options`, or when `__other__` is
   *  selected. */
  text: string
}

/**
 * Alternate state of the composer for an AskUserQuestion call. Renders
 * inside `.composer > .composer-shell` with the same frame, border,
 * and foot as the normal send-a-message state — what changes is the
 * inner content: a question header strip replaces the mode row, an
 * option chip row sits where the textarea top would be, and the send
 * button's label flips to "send answer". The composer isn't swapped
 * for a new component; it's the same container in a different mode.
 *
 * Mirrors Claude Code's AskUserQuestion TUI: each question has a
 * header chip + question text, 2–4 options with an auto-added "Other"
 * for free text, and multi-question calls step through a progress
 * tracker + final review page before submit.
 */
export function AnswerStage({ question, busy, onAnswer, onDismiss }: AnswerStageProps) {
  const items = question.questions
  const multiQuestion = items.length > 1
  const [index, setIndex] = useState(0)
  const [drafts, setDrafts] = useState<SingleAnswerDraft[]>(() =>
    items.map(() => ({ selected: [], text: '' })),
  )
  const [review, setReview] = useState(false)
  const [submitError, setSubmitError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const taRef = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    setIndex(0)
    setDrafts(items.map(() => ({ selected: [], text: '' })))
    setReview(false)
    setSubmitError(null)
    setSubmitting(false)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [question.callId])

  const current = items[index]
  const draft = drafts[index] ?? { selected: [], text: '' }
  const usesOptions = !!current?.options?.length
  const allowsMulti = !!current?.multiSelect
  const otherSelected = draft.selected.includes(OTHER_VALUE)
  const showTextarea = !usesOptions || otherSelected

  const currentAnswer = useMemo(() => resolveAnswer(draft, current), [draft, current])
  const canAdvance = !!currentAnswer && !busy && !submitting
  const isLast = index === items.length - 1
  const showReview = multiQuestion && review

  function setDraft(next: SingleAnswerDraft) {
    setDrafts((cur) => {
      const copy = cur.slice()
      copy[index] = next
      return copy
    })
  }

  function toggleOption(label: string) {
    if (!allowsMulti) {
      setDraft({ selected: [label], text: label === OTHER_VALUE ? draft.text : '' })
      return
    }
    const already = draft.selected.includes(label)
    const nextSelected = already
      ? draft.selected.filter((l) => l !== label)
      : [...draft.selected, label]
    setDraft({ ...draft, selected: nextSelected })
  }

  async function submit() {
    if (submitting) return
    const payload: Record<string, string> = {}
    items.forEach((q, i) => {
      const a = resolveAnswer(drafts[i] ?? { selected: [], text: '' }, q)
      payload[q.question] = a ?? ''
    })
    for (const q of items) {
      if (!payload[q.question]) {
        setSubmitError(`No answer for: "${q.question}"`)
        return
      }
    }
    setSubmitting(true)
    setSubmitError(null)
    try {
      const result =
        items.length === 1
          ? onAnswer(question.callId, payload[items[0].question])
          : onAnswer(question.callId, payload)
      if (result && typeof (result as Promise<void>).then === 'function') {
        await result
      }
    } catch (err) {
      setSubmitError(err instanceof Error ? err.message : String(err))
      setSubmitting(false)
    }
  }

  function advance() {
    if (!canAdvance) return
    if (!isLast) {
      setIndex((i) => Math.min(i + 1, items.length - 1))
      return
    }
    if (multiQuestion) {
      setReview(true)
      return
    }
    void submit()
  }

  function onKey(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && canAdvance) {
      e.preventDefault()
      advance()
    }
  }

  if (!current) return null

  // The top strip replaces the composer's mode row with the question
  // itself: header chip on the left (like a setting name), question
  // text fills the middle, progress + dismiss sit on the right. Same
  // visual weight as the "running · paused" status strip so the shell
  // reads as "composer in answer-mode" rather than a new panel.
  const headerStrip = (
    <div className="composer-status composer-status-ask" data-state="asking">
      {showReview ? (
        <span className="composer-status-kicker">review</span>
      ) : (
        current.header && (
          <span className="composer-status-kicker">{current.header}</span>
        )
      )}
      <span className="composer-status-q">
        {showReview ? 'Confirm your answers, then submit.' : current.question}
      </span>
      <span style={{ flex: 1 }} />
      {multiQuestion && !showReview && (
        <span className="composer-status-progress">
          {index + 1}/{items.length}
        </span>
      )}
      <button
        type="button"
        className="composer-status-dismiss"
        onClick={() => onDismiss(question.callId)}
        title="Dismiss — doesn't send a reply to the agent"
        aria-label="Dismiss"
        disabled={submitting}
      >
        <IconX size={10} />
      </button>
    </div>
  )

  const optionsRow =
    !showReview && usesOptions ? (
      <div className="composer-options" role={allowsMulti ? 'group' : 'radiogroup'}>
        {current.options!.map((opt) => {
          const on = draft.selected.includes(opt.label)
          return (
            <button
              key={opt.label}
              type="button"
              className="composer-option"
              role={allowsMulti ? 'checkbox' : 'radio'}
              aria-checked={on}
              data-on={on || undefined}
              onClick={() => toggleOption(opt.label)}
              disabled={busy || submitting}
              title={opt.description || opt.label}
            >
              <span className="composer-option-label">{opt.label}</span>
            </button>
          )
        })}
        <button
          type="button"
          className="composer-option"
          role={allowsMulti ? 'checkbox' : 'radio'}
          aria-checked={otherSelected}
          data-on={otherSelected || undefined}
          data-other="true"
          onClick={() => toggleOption(OTHER_VALUE)}
          disabled={busy || submitting}
          title="Type a free-text answer"
        >
          <span className="composer-option-label">Other…</span>
        </button>
      </div>
    ) : null

  const reviewBody = showReview ? (
    <ol className="composer-review">
      {items.map((q, i) => {
        const a = resolveAnswer(drafts[i] ?? { selected: [], text: '' }, q) ?? ''
        return (
          <li key={q.question} className="composer-review-row">
            {q.header && <span className="composer-review-kicker">{q.header}</span>}
            <div className="composer-review-q">{q.question}</div>
            <div className="composer-review-a" data-empty={!a || undefined}>
              {a || '(no answer)'}
            </div>
            <button
              type="button"
              className="composer-review-edit"
              onClick={() => {
                if (submitting) return
                setReview(false)
                setIndex(i)
              }}
              disabled={submitting}
            >
              edit
            </button>
          </li>
        )
      })}
    </ol>
  ) : null

  const textareaBody =
    !showReview && showTextarea ? (
      <textarea
        ref={taRef}
        className="composer-input"
        placeholder={
          usesOptions
            ? 'Your answer…'
            : `Your answer. ${IS_MAC ? '⌘↵' : 'Ctrl+↵'} to send.`
        }
        value={draft.text}
        onChange={(e) => setDraft({ ...draft, text: e.target.value })}
        onKeyDown={onKey}
        disabled={busy || submitting}
        rows={2}
        autoFocus
      />
    ) : null

  const hint = showReview
    ? 'Review every answer, then submit.'
    : multiQuestion
      ? isLast
        ? 'Last question.'
        : `${items.length - index - 1} more after this.`
      : `${IS_MAC ? '⌘↵' : 'Ctrl+↵'} sends`

  const sendLabel = submitting
    ? 'sending…'
    : showReview || !multiQuestion
      ? 'send answer'
      : isLast
        ? 'review'
        : 'next'

  return (
    <div className="composer" data-answer="true">
      <div className="composer-shell" data-mode="answer" data-runtime="true">
        {headerStrip}
        {multiQuestion && !showReview && (
          <div className="composer-steps" role="list">
            {items.map((q, i) => {
              const done = !!resolveAnswer(drafts[i] ?? { selected: [], text: '' }, q)
              const here = i === index
              const state = here ? 'current' : done ? 'done' : 'pending'
              return (
                <button
                  key={q.question}
                  type="button"
                  role="listitem"
                  className="composer-step"
                  data-state={state}
                  onClick={() => {
                    if (submitting) return
                    setIndex(i)
                  }}
                  disabled={submitting}
                  title={q.header || q.question}
                >
                  {i + 1}
                </button>
              )
            })}
          </div>
        )}
        {optionsRow}
        {reviewBody}
        {textareaBody}
        {submitError && (
          <div className="composer-ask-error" role="alert">
            <strong>Couldn't send answer:</strong> {submitError}
          </div>
        )}
        <div className="composer-foot">
          <span className="composer-hint">{hint}</span>
          <span style={{ flex: 1 }} />
          {showReview && (
            <button
              type="button"
              className="composer-back"
              onClick={() => {
                setReview(false)
                setIndex(items.length - 1)
              }}
              disabled={submitting}
            >
              back
            </button>
          )}
          {!showReview && index > 0 && (
            <button
              type="button"
              className="composer-back"
              onClick={() => setIndex((i) => Math.max(0, i - 1))}
              disabled={busy || submitting}
            >
              back
            </button>
          )}
          <button
            type="button"
            className="composer-send"
            onClick={showReview ? () => void submit() : advance}
            disabled={showReview ? submitting : !canAdvance}
            data-mode="answer"
          >
            {sendLabel}
          </button>
        </div>
      </div>
    </div>
  )
}

function resolveAnswer(
  draft: SingleAnswerDraft,
  q: PendingQuestion['questions'][number] | undefined,
): string | null {
  if (!q) return null
  const usesOptions = !!q.options?.length
  const allowsMulti = !!q.multiSelect
  if (!usesOptions) {
    const t = draft.text.trim()
    return t ? t : null
  }
  const nonOther = draft.selected.filter((l) => l !== OTHER_VALUE)
  const otherSelected = draft.selected.includes(OTHER_VALUE)
  if (allowsMulti) {
    const otherText = otherSelected ? draft.text.trim() : ''
    const parts = [...nonOther]
    if (otherSelected) {
      if (!otherText) return null
      parts.push(otherText)
    }
    return parts.length ? parts.join(', ') : null
  }
  if (otherSelected) {
    const t = draft.text.trim()
    return t ? t : null
  }
  return nonOther[0] ?? null
}
