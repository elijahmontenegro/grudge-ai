import { useState } from 'react'
import { IconChevron } from '@/components/atoms/icons'
import { parseAskUserQuestionArgs } from '@/hooks/useToolExecutions'

interface AskQuestionRowProps {
  /** The tool call's raw arguments JSON. Decoded for the Q: display. */
  args: string
  /** The tool result JSON — `{answers: {question: answer}}` when
   *  answered, empty / `{answers: {}}` when the agent timed out. */
  result: string
  status: 'ok' | 'running' | 'error'
  /** True when this is a live pending question (still waiting on user).
   *  `ToolExecutionSub` marks these with `waiting_for_user`; the actual
   *  interactive UI lives in AnswerStage, this row just labels the
   *  history entry. */
  pending?: boolean
}

/**
 * Specialized tool-call row for AskUserQuestion. Renders as a compact
 * Q/A pair instead of the generic tool-name + JSON-args + status trio.
 *
 *   asking   · "Which deploy target?"       (live, no answer yet)
 *   answered · "Which deploy target?"
 *             → production                  (answered historic)
 *   unanswered · "Which deploy target?"     (historic, never got an answer)
 *
 * Keeps the expand/collapse affordance for multi-question calls where
 * the summary row can't fit every Q/A pair — click to see the full
 * ordered list.
 */
export function AskQuestionRow({ args, result, status, pending }: AskQuestionRowProps) {
  const questions = parseAskUserQuestionArgs(args) ?? []
  const answers = parseAnswers(result)
  const multi = questions.length > 1
  const answered = Object.values(answers).some((a) => a.trim().length > 0)
  const kind: 'asking' | 'answered' | 'unanswered' = pending
    ? 'asking'
    : answered
      ? 'answered'
      : 'unanswered'
  const [open, setOpen] = useState(multi && pending)

  const first = questions[0]
  const firstAnswer = first ? answers[first.question] : undefined

  // Single-question calls never expand — the one Q/A fits in the row.
  // Multi-question calls get the chevron toggle so the user can read
  // every pair in order without re-interpreting raw JSON.
  return (
    <div className="askq" data-kind={kind} data-open={multi && open ? 'true' : undefined}>
      <button
        type="button"
        className="askq-head"
        onClick={() => multi && setOpen((v) => !v)}
        disabled={!multi}
      >
        {multi && (
          <span className="askq-chev">
            <IconChevron size={10} />
          </span>
        )}
        <span className="askq-kind">{kind}</span>
        <span className="askq-question" title={first?.question}>
          {first?.question ?? '(no question)'}
        </span>
        {multi && <span className="askq-count">{questions.length} questions</span>}
        {kind === 'answered' && !multi && firstAnswer && (
          <span className="askq-answer" title={firstAnswer}>
            → {firstAnswer}
          </span>
        )}
        {status === 'error' && <span className="askq-status">error</span>}
      </button>
      {multi && open && (
        <ol className="askq-list">
          {questions.map((q) => {
            const a = answers[q.question]
            return (
              <li key={q.question}>
                <div className="askq-list-q">{q.question}</div>
                <div className="askq-list-a" data-empty={!a || undefined}>
                  {a && a.trim() ? a : '(no answer)'}
                </div>
              </li>
            )
          })}
        </ol>
      )}
    </div>
  )
}

function parseAnswers(raw: string): Record<string, string> {
  if (!raw) return {}
  try {
    const parsed = JSON.parse(raw) as unknown
    if (parsed && typeof parsed === 'object') {
      const obj = parsed as Record<string, unknown>
      if (obj.answers && typeof obj.answers === 'object') {
        const out: Record<string, string> = {}
        for (const [k, v] of Object.entries(obj.answers as Record<string, unknown>)) {
          out[k] = typeof v === 'string' ? v : String(v ?? '')
        }
        return out
      }
      if (typeof obj.response === 'string') {
        // Legacy single-question result shape from the pre-refactor backend.
        // Not reachable for new calls but keeps historic threads readable.
        return { __legacy: obj.response }
      }
    }
  } catch {
    // Treat the whole raw string as a single legacy answer.
    return raw.trim() ? { __legacy: raw } : {}
  }
  return {}
}
