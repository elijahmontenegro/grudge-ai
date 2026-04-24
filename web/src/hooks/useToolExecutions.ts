import { useCallback, useEffect, useState } from 'react'
import { useMutation, useSubscription } from '@apollo/client/react'
import {
  ANSWER_QUESTION,
  APPROVE_TOOL_CALL,
  DENY_TOOL_CALL,
  TOOL_EXECUTION_SUB,
} from '@/graphql/operations'
import type {
  AnswerQuestionMutation,
  AnswerQuestionMutationVariables,
  ApproveToolCallMutation,
  ApproveToolCallMutationVariables,
  DenyToolCallMutation,
  DenyToolCallMutationVariables,
  ToolExecutionSubSubscription,
  ToolExecutionSubSubscriptionVariables,
} from '@/graphql/generated/types'

export interface PendingToolCall {
  callId: string
  toolName: string
  arguments: string
  arrivedAt: number
}

export interface AskUserOption {
  label: string
  description?: string
}
export interface AskUserQuestionItem {
  question: string
  header?: string
  options?: AskUserOption[]
  multiSelect?: boolean
}
export interface PendingQuestion {
  callId: string
  /** Full structured question array decoded from the tool's arguments
   *  JSON. At least one entry is always present — the first is the one
   *  the UI prompts for first. */
  questions: AskUserQuestionItem[]
  arrivedAt: number
}

/** Parse the JSON payload the backend publishes for AskUserQuestion.
 *  Accepts two shapes: the structured `{questions: [...]}` form and the
 *  legacy plain `{question: "..."}` form (in case an old tool call is
 *  still live across a deploy). Returns null when neither parses. */
export function parseAskUserQuestionArgs(raw: string): AskUserQuestionItem[] | null {
  try {
    const parsed = JSON.parse(raw) as unknown
    if (parsed && typeof parsed === 'object') {
      const obj = parsed as Record<string, unknown>
      if (Array.isArray(obj.questions)) {
        const items = (obj.questions as unknown[])
          .map((q) => normalizeQuestion(q))
          .filter((q): q is AskUserQuestionItem => q !== null)
        return items.length > 0 ? items : null
      }
      if (typeof obj.question === 'string') {
        const q = normalizeQuestion(obj)
        return q ? [q] : null
      }
    }
  } catch {
    // fall through — not JSON; treat the whole thing as a bare question
  }
  if (raw.trim()) return [{ question: raw }]
  return null
}

function normalizeQuestion(raw: unknown): AskUserQuestionItem | null {
  if (!raw || typeof raw !== 'object') return null
  const q = raw as Record<string, unknown>
  if (typeof q.question !== 'string' || !q.question.trim()) return null
  const out: AskUserQuestionItem = { question: q.question }
  if (typeof q.header === 'string' && q.header.trim()) out.header = q.header
  if (Array.isArray(q.options)) {
    const opts: AskUserOption[] = []
    for (const o of q.options) {
      if (!o || typeof o !== 'object') continue
      const ro = o as Record<string, unknown>
      if (typeof ro.label !== 'string' || !ro.label.trim()) continue
      const opt: AskUserOption = { label: ro.label }
      if (typeof ro.description === 'string') opt.description = ro.description
      opts.push(opt)
    }
    if (opts.length) out.options = opts
  }
  if (typeof q.multiSelect === 'boolean') out.multiSelect = q.multiSelect
  return out
}

export interface LiveToolCall {
  callId: string
  toolName: string
  arguments: string
  status: 'running' | 'completed' | 'error' | 'pending' | 'waiting_for_user'
  isError: boolean
  result: string | null
}

export interface ToolExecutionsState {
  /** Gated tool calls waiting on user approve/deny. */
  pending: PendingToolCall[]
  /** AskUserQuestion events waiting on user answer. */
  questions: PendingQuestion[]
  /** Every tool lifecycle event on this thread, for live display during
   *  streaming. Resets on turn boundary via `resetKey`. */
  live: LiveToolCall[]
  approve: (callId: string) => Promise<void>
  deny: (callId: string, reason?: string) => Promise<void>
  /** Submit the user's reply to an AskUserQuestion. Pass a plain string
   *  for a single-question tool call; pass a `{question: answer}` map
   *  for a multi-question call — the resolver parses the JSON on the
   *  other side. */
  answer: (callId: string, answer: string | Record<string, string>) => Promise<void>
  /** Clear a stale question locally without sending any answer to the
   *  backend. Used when the card is stuck (agent restarted, timeout
   *  expired) and the user wants to dismiss without committing a reply. */
  dismissQuestion: (callId: string) => void
  busy: boolean
  error: string | null
}

/**
 * Single subscription point for TOOL_EXECUTION_SUB. Exposes three views of
 * the same event stream:
 *   - pending: gated calls waiting for approve/deny (status="pending")
 *   - questions: AskUserQuestion prompts (status="waiting_for_user")
 *   - live: every lifecycle event for streaming display
 *
 * Previously `useToolApprovals` and `useLiveToolCalls` each opened their
 * own subscription — same events delivered twice per thread. Consolidated.
 *
 * `resetKey` — bumping it clears the `live` list (used at stream boundary).
 * Pending + questions are not reset since their resolution is driven by
 * terminal events, not turn boundaries.
 */
export function useToolExecutions(
  threadId: string,
  resetKey: number | string = 0,
): ToolExecutionsState {
  const [pending, setPending] = useState<PendingToolCall[]>([])
  const [questions, setQuestions] = useState<PendingQuestion[]>([])
  const [live, setLive] = useState<LiveToolCall[]>([])

  useSubscription<ToolExecutionSubSubscription, ToolExecutionSubSubscriptionVariables>(
    TOOL_EXECUTION_SUB,
    {
      variables: { threadId },
      skip: !threadId,
      onData: ({ data }) => {
        const ev = data.data?.toolExecution
        if (!ev) return

        // --- live list: upsert by callId, any status ---
        setLive((cur) => {
          const status = (ev.status as LiveToolCall['status']) ?? 'running'
          const idx = cur.findIndex((c) => c.callId === ev.callId)
          const next: LiveToolCall = {
            callId: ev.callId,
            toolName: ev.toolName,
            arguments: ev.arguments,
            status,
            isError: ev.isError ?? false,
            result: ev.result ?? null,
          }
          if (idx < 0) return [...cur, next]
          const copy = cur.slice()
          copy[idx] = next
          return copy
        })

        // --- pending list: gated tool approvals ---
        if (ev.status === 'pending') {
          setPending((cur) => {
            if (cur.some((c) => c.callId === ev.callId)) return cur
            return [
              ...cur,
              {
                callId: ev.callId,
                toolName: ev.toolName,
                arguments: ev.arguments,
                arrivedAt: Date.now(),
              },
            ]
          })
          return
        }

        // --- question list: AskUserQuestion prompts ---
        if (ev.status === 'waiting_for_user' && ev.toolName === 'AskUserQuestion') {
          const questions = parseAskUserQuestionArgs(ev.arguments)
          if (!questions) return
          setQuestions((cur) => {
            if (cur.some((q) => q.callId === ev.callId)) return cur
            return [
              ...cur,
              { callId: ev.callId, questions, arrivedAt: Date.now() },
            ]
          })
          return
        }

        // Any other status clears whichever pending / question list contains it.
        setPending((cur) => cur.filter((c) => c.callId !== ev.callId))
        setQuestions((cur) => cur.filter((q) => q.callId !== ev.callId))
      },
    },
  )

  // Thread changed → reset everything.
  useEffect(() => {
    setPending([])
    setQuestions([])
    setLive([])
  }, [threadId])

  // Turn boundary → reset only the live list. Pending + questions survive
  // across turns because their lifecycle is driven by terminal events, not
  // turn starts.
  useEffect(() => {
    setLive([])
  }, [resetKey])

  const [approveMut, approveRes] = useMutation<
    ApproveToolCallMutation,
    ApproveToolCallMutationVariables
  >(APPROVE_TOOL_CALL)
  const [denyMut, denyRes] = useMutation<DenyToolCallMutation, DenyToolCallMutationVariables>(
    DENY_TOOL_CALL,
  )
  const [answerMut, answerRes] = useMutation<
    AnswerQuestionMutation,
    AnswerQuestionMutationVariables
  >(ANSWER_QUESTION)

  const approve = useCallback(
    async (callId: string) => {
      await approveMut({ variables: { callId } })
      setPending((cur) => cur.filter((c) => c.callId !== callId))
    },
    [approveMut],
  )

  const deny = useCallback(
    async (callId: string, reason?: string) => {
      await denyMut({ variables: { callId, reason: reason ?? null } })
      setPending((cur) => cur.filter((c) => c.callId !== callId))
    },
    [denyMut],
  )

  const answer = useCallback(
    async (callId: string, answerPayload: string | Record<string, string>) => {
      // Only clear from local state AFTER the mutation both resolves
      // AND returns `true` for `answerQuestion`. Apollo's `await
      // mutate()` does NOT throw on GraphQL errors by default — it
      // resolves with `{ data, errors }` — so a stale callId (runner
      // restart, timeout, already-answered) returns `errors` quietly
      // and `answerQuestion: false` in data, which previously looked
      // like success to the optimistic-dismiss path. Now we unwrap
      // both and throw so the AnswerStage catches + surfaces it.
      const payload =
        typeof answerPayload === 'string' ? answerPayload : JSON.stringify(answerPayload)
      const res = await answerMut({ variables: { callId, answer: payload } })
      if (res.error) {
        throw new Error(res.error.message)
      }
      if (res.data?.answerQuestion !== true) {
        throw new Error(
          'Runner rejected the answer — the question is likely stale (restart or 5-minute timeout).',
        )
      }
      setQuestions((cur) => cur.filter((q) => q.callId !== callId))
    },
    [answerMut],
  )

  // Manual dismiss without sending an answer — useful when the question
  // is stale (agent was restarted, timed out, or the user wants to
  // ignore it). Never hits the backend.
  const dismissQuestion = useCallback((callId: string) => {
    setQuestions((cur) => cur.filter((q) => q.callId !== callId))
  }, [])

  return {
    pending,
    questions,
    live,
    approve,
    deny,
    answer,
    dismissQuestion,
    busy: approveRes.loading || denyRes.loading || answerRes.loading,
    error:
      approveRes.error?.message ??
      denyRes.error?.message ??
      answerRes.error?.message ??
      null,
  }
}
