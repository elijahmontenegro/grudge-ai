import { useEffect, useMemo, useRef } from 'react'
import { StreamScrollbar, type ScrollbarIndicator } from './StreamScrollbar'
import {
  Composer,
  type AutonomousDuration,
  type ComposerMode,
  type ComposerScope,
} from '@/features/composer/Composer'
import { StreamingTurn } from './StreamingTurn'
import { EmptyThread } from '@/features/thread/EmptyThread'
import { Turn } from '@/features/thread/Turn'
import { PlanApprovalCard } from '@/features/plan/PlanApprovalCard'
import { ToolApprovalSlot } from '@/features/thread/ToolApprovalSlot'
import { AnswerStage } from '@/features/thread/AnswerStage'
import { AgentMode, AgentStatus } from '@/graphql/generated/types'
import type { PendingQuestion, PendingToolCall } from '@/state/toolExecutions'
import type { StreamState } from '@/hooks/useSendAndStream'
import type { LiveSubagent } from '@/hooks/useSubagentProgress'
import type { LiveToolCall } from '@/state/toolExecutions'
import type { AttachmentMeta } from '@/hooks/useAttachments'
import type { ThreadDetail, ThreadMessage } from '@/hooks/useThreadMessages'

interface ThreadViewProps {
  thread: ThreadDetail
  messages: ThreadMessage[]
  parentName?: string | null
  streaming: boolean
  stream: StreamState
  subagents?: LiveSubagent[]
  liveTools?: LiveToolCall[]
  onSend: (text: string, attachments?: AttachmentMeta[]) => void
  onStartAutonomous?: (text: string, duration: AutonomousDuration, attachments?: AttachmentMeta[]) => void
  scope: ComposerScope
  setScope: (s: ComposerScope) => void
  mode: ComposerMode
  setMode: (m: ComposerMode) => void
  duration: AutonomousDuration
  setDuration: (d: AutonomousDuration) => void
  /** If set, scroll to the turn containing this message id on first paint. */
  focusMessageId?: string
  /** Edit handler — lifted from the page so a long thread doesn't instantiate
   *  one Apollo mutation hook per turn. */
  onEditTurn?: (position: number, newContent: string) => Promise<string | null>
  editInFlight?: boolean
  // Artifacts panel (right-side collapsible).
  artifactsCollapsed: boolean
  onToggleArtifacts: () => void
  livePlanContent?: string | null
  // Plan approval card (inline in chat). Rendered when agentMode === Plan
  // and a plan has been emitted. Actions live here, not in the panel.
  agentMode: AgentMode
  planApproving: boolean
  planRejecting: boolean
  planEditing: boolean
  onApprovePlan: (autonomous: boolean) => void
  onRejectPlan: (feedback: string) => void
  onEditPlan: (content: string) => Promise<void> | void
  // Agent runtime status (folded into Composer so the running tick +
  // pause/resume/stop live adjacent to where the user types).
  agentStatus: AgentStatus
  agentIsAutonomous: boolean
  agentElapsed: string | null
  agentRetry?: {
    attempt: number
    maxAttempts: number
    error: string | null
    nextDelayMs: number
    final: boolean
  } | null
  onPauseAgent: () => void
  onResumeAgent: (correction?: string) => void
  onStopAgent: () => void
  agentControlsPending: boolean
  // AskUserQuestion prompts — rendered inline at the end of the stream
  // so the "agent asks ..." card reads as the next beat in the
  // conversation, next to the AskUserQuestion tool call, rather than
  // floating pinned at the top of the page.
  pendingQuestions?: PendingQuestion[]
  onAnswerQuestion?: (
    callId: string,
    answer: string | Record<string, string>,
  ) => Promise<void>
  onDismissQuestion?: (callId: string) => void
  questionsBusy?: boolean
  // Tool-approval gates — per-call entries waiting on user approve/deny.
  // Rendered inside the matching tool's slot via renderToolExtras (see
  // CLAUDE.md: pre-execution permissions belong with the call they
  // gate, not floating).
  pendingApprovals?: PendingToolCall[]
  onApproveTool?: (callId: string) => void
  onDenyTool?: (callId: string, reason?: string) => void
  approvalsBusy?: boolean
}

interface TurnData {
  prompt: ThreadMessage
  /** All assistant messages produced in response to this prompt, in order.
   *  Agent turns commonly span many messages — one per thinking step / tool
   *  invocation — so grouping one-to-one would drop most of the work. */
  responses: ThreadMessage[]
  pending?: boolean
}

export function ThreadView({
  thread,
  messages,
  parentName,
  streaming,
  stream,
  subagents,
  liveTools,
  onSend,
  onStartAutonomous,
  scope,
  setScope,
  mode,
  setMode,
  duration,
  setDuration,
  focusMessageId,
  onEditTurn,
  editInFlight,
  artifactsCollapsed,
  onToggleArtifacts,
  livePlanContent,
  agentMode,
  planApproving,
  planRejecting,
  planEditing,
  onApprovePlan,
  onRejectPlan,
  onEditPlan,
  agentStatus,
  agentIsAutonomous,
  agentElapsed,
  agentRetry,
  onPauseAgent,
  onResumeAgent,
  onStopAgent,
  agentControlsPending,
  pendingQuestions,
  onAnswerQuestion,
  onDismissQuestion,
  questionsBusy,
  pendingApprovals,
  onApproveTool,
  onDenyTool,
  approvalsBusy,
}: ThreadViewProps) {
  // The corpus is the messages array, not bundled into thread.
  const corpus = messages

  const turns = useMemo<TurnData[]>(() => {
    const out: TurnData[] = []
    for (let i = 0; i < corpus.length; i++) {
      const m = corpus[i]
      if (m.role !== 'user') continue
      const responses: ThreadMessage[] = []
      let j = i + 1
      while (j < corpus.length && corpus[j].role === 'assistant') {
        responses.push(corpus[j])
        j++
      }
      out.push({
        prompt: m,
        responses,
        pending: responses.length === 0,
      })
    }
    return out
  }, [corpus])

  const streamRef = useRef<HTMLDivElement>(null)
  const lastTurnRef = useRef<HTMLDivElement>(null)
  // If the user has scrolled up to read earlier history we don't yank them
  // back to the bottom on every delta / new message. Re-engage auto-scroll
  // when they scroll near the bottom on their own.
  const autoScrollRef = useRef(true)

  useEffect(() => {
    const el = streamRef.current
    if (!el) return
    function onScroll() {
      if (!el) return
      const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 80
      autoScrollRef.current = nearBottom
    }
    el.addEventListener('scroll', onScroll, { passive: true })
    return () => el.removeEventListener('scroll', onScroll)
  }, [])

  function scrollToBottom(immediate = false) {
    const el = streamRef.current
    if (!el) return
    if (immediate) {
      const prev = el.style.scrollBehavior
      el.style.scrollBehavior = 'auto'
      el.scrollTop = el.scrollHeight
      el.style.scrollBehavior = prev || ''
    } else {
      el.scrollTop = el.scrollHeight
    }
  }

  // Jump to last turn when the thread changes — always, regardless of prior
  // user scroll state (navigating is a reset).
  useEffect(() => {
    autoScrollRef.current = true
    // Reset corpus-length memo so the subsequent length effect doesn't
    // misfire a scrollToBottom the first time the new thread's corpus loads.
    prevCorpusLenRef.current = corpus.length
    if (lastTurnRef.current && streamRef.current) {
      const el = lastTurnRef.current
      streamRef.current.scrollTop = el.offsetTop - streamRef.current.offsetTop - 12
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [thread.id])

  // If navigated here with a focusMessageId (e.g. from a palette search hit),
  // scroll to that turn once the corpus has loaded, then briefly flag it
  // with data-focused so CSS can flash the background — gives the user
  // visual confirmation of where they landed.
  useEffect(() => {
    if (!focusMessageId || corpus.length === 0) return
    const idx = turns.findIndex(
      (t) => t.prompt.id === focusMessageId || t.responses.some((r) => r.id === focusMessageId),
    )
    if (idx < 0) return
    autoScrollRef.current = false
    const tid = setTimeout(() => {
      const el = streamRef.current?.querySelectorAll('[data-turn]')[idx] as HTMLElement | undefined
      if (el && streamRef.current) {
        streamRef.current.scrollTop = el.offsetTop - streamRef.current.offsetTop - 12
        el.setAttribute('data-focused', 'true')
        setTimeout(() => el.removeAttribute('data-focused'), 1600)
      }
    }, 50)
    return () => clearTimeout(tid)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusMessageId, corpus.length])

  // Send pushes the view to the bottom unconditionally — the user just
  // submitted, they want to see their turn land and the response begin.
  // Catches both initial submit (streaming true) AND new-turn arrivals from
  // refetch after mutation (corpus length grows).
  useEffect(() => {
    if (!streaming) return
    autoScrollRef.current = true
    scrollToBottom(true)
  }, [streaming])

  // Track last-seen corpus length so we only auto-scroll on GROWTH.
  // Without this, the first render of every thread also hits the length
  // effect and overrides the thread.id-jump-to-last-turn placement.
  const prevCorpusLenRef = useRef(corpus.length)
  useEffect(() => {
    const grew = corpus.length > prevCorpusLenRef.current
    prevCorpusLenRef.current = corpus.length
    if (grew && autoScrollRef.current) scrollToBottom()
  }, [corpus.length])

  // While the stream is active, follow deltas / thinking as they
  // accumulate so the caret stays on-screen. Tool calls flow through
  // ToolExecution subscription rather than the stream — their list
  // belongs to the live-tools effect downstream.
  useEffect(() => {
    if (!streaming || !autoScrollRef.current) return
    scrollToBottom()
  }, [streaming, stream.text.length, stream.thinking.length])

  // Interactive tool outputs render inside their tool's own slot (see
  // CLAUDE.md: "Tool outputs render in their tool's slot"). Compute,
  // once, which tool-call ids get which interactive extras:
  //   - AskUserQuestion tool calls with a pending question → answer form
  //   - The most recent ExitPlanMode when plan mode is awaiting approval → plan approval
  const pendingQuestionByCallId = useMemo(() => {
    const m = new Map<string, PendingQuestion>()
    for (const q of pendingQuestions ?? []) m.set(q.callId, q)
    return m
  }, [pendingQuestions])

  const pendingApprovalByCallId = useMemo(() => {
    const m = new Map<string, PendingToolCall>()
    for (const p of pendingApprovals ?? []) m.set(p.callId, p)
    return m
  }, [pendingApprovals])

  const latestExitPlanCallId = useMemo(() => {
    // Walk newest-to-oldest; the freshest ExitPlanMode owns the approval card.
    for (let i = corpus.length - 1; i >= 0; i--) {
      const msg = corpus[i]
      for (const call of msg.toolCalls) {
        if (call.name === 'ExitPlanMode') return call.id
      }
    }
    return null
  }, [corpus])

  const showPlanApproval =
    !streaming && agentMode === AgentMode.Plan && !!livePlanContent && !!latestExitPlanCallId

  // Overview-ruler indicators — compact markers on the scrollbar track
  // at each message's fractional y-position. Tool errors, awaiting-
  // answer questions, and the pending plan approval map to visible
  // signals the user should skim to. Approximated by message index /
  // total; pixel-accurate positioning would require measuring each
  // turn's DOM offset which isn't worth the cost here.
  const indicators = useMemo<ScrollbarIndicator[]>(() => {
    const out: ScrollbarIndicator[] = []
    const n = corpus.length || 1
    corpus.forEach((m, i) => {
      const frac = i / n
      // Every user message becomes a structural marker — this is the
      // "chapter rule" for the conversation and the dominant signal in
      // a long thread. Analogous to VS Code's git decorations or find
      // matches being the bulk of the ruler lanes.
      if (m.role === 'user') {
        out.push({ frac, kind: 'user', title: `#${m.position} you` })
      }
      for (const tr of m.toolResults) {
        if (
          /^\s*(error|failed|exception|traceback)/i.test(tr.content) ||
          /\b(connection refused|timed? out|permission denied|actively refused)\b/i.test(tr.content)
        ) {
          out.push({ frac, kind: 'error', title: 'tool error' })
          break
        }
      }
      for (const call of m.toolCalls) {
        if (call.name === 'AskUserQuestion' && pendingQuestionByCallId.has(call.id)) {
          out.push({ frac, kind: 'question', title: 'awaiting answer' })
        }
        if (call.name === 'ExitPlanMode' && showPlanApproval && call.id === latestExitPlanCallId) {
          out.push({ frac, kind: 'plan', title: 'plan awaiting approval' })
        }
      }
    })
    return out
  }, [corpus, pendingQuestionByCallId, showPlanApproval, latestExitPlanCallId])

  const renderToolExtras = (call: {
    id: string
    name: string
    arguments: string
    status: string
    result: string | null
  }) => {
    // AskUserQuestion no longer renders an interactive form inline — the
    // live question (if any) takes over the composer slot via
    // `AnswerStage`. The tool row itself stays as a standard tool-call
    // entry so historic reads as "Q: … / A: …" naturally from the tool
    // args + result (see CLAUDE.md: "Tool outputs render in their tool's
    // slot" — here the slot is the composer, not a floating card).
    if (call.name === 'ExitPlanMode' && showPlanApproval && call.id === latestExitPlanCallId) {
      return {
        forceOpen: true,
        extras: (
          <PlanApprovalCard
            planContent={livePlanContent!}
            approving={planApproving}
            rejecting={planRejecting}
            editing={planEditing}
            onApprove={onApprovePlan}
            onReject={onRejectPlan}
            onEdit={onEditPlan}
            onViewPlan={() => {
              if (artifactsCollapsed) onToggleArtifacts()
            }}
          />
        ),
      }
    }
    const pendingApproval = pendingApprovalByCallId.get(call.id)
    if (pendingApproval && onApproveTool && onDenyTool) {
      return {
        forceOpen: true,
        extras: (
          <ToolApprovalSlot
            pending={pendingApproval}
            busy={!!approvalsBusy}
            onApprove={onApproveTool}
            onDeny={onDenyTool}
          />
        ),
      }
    }
    return null
  }

  // Layout intent: the chat column and its composer are one cohesive
  // unit — stream scrolls, composer parks at the bottom of that column.
  // The artifacts panel occupies its own full-height column beside them.
  // Two panels, not three (old layout had a third row: composer spanning
  // the entire canvas below the artifacts panel).
  return (
    <div className="thread-canvas">
      <div ref={streamRef} className="thread-stream scroll">
          {turns.length === 0 && <EmptyThread thread={thread} parentName={parentName} />}
          {turns.map((t, i) => {
            const isLast = i === turns.length - 1
            return (
              <div key={t.prompt.id} data-turn={i} ref={isLast ? lastTurnRef : null}>
                <Turn
                  prompt={t.prompt}
                  threadId={thread.id}
                  responses={t.responses}
                  pendingLabel={
                    t.pending && !streaming ? 'Waiting for agent.' : undefined
                  }
                  last={isLast}
                  editable={!streaming}
                  onEdit={onEditTurn}
                  editInFlight={editInFlight}
                  renderToolExtras={renderToolExtras}
                />
              </div>
            )
          })}
          {streaming && (
            <StreamingTurn
              stream={stream}
              subagents={subagents}
              liveTools={liveTools}
              pendingApprovals={pendingApprovals}
              onApproveTool={onApproveTool}
              onDenyTool={onDenyTool}
              approvalsBusy={approvalsBusy}
            />
          )}
        </div>
        {/* Composer vs. AnswerStage: when the agent is blocked on an
            AskUserQuestion, the composer slot flips to the answer UI.
            Once the user submits (or dismisses) the answer the composer
            re-mounts unchanged — draft text is preserved in
            localStorage so nothing the user was typing is lost. */}
        {pendingQuestions && pendingQuestions.length > 0 && onAnswerQuestion ? (
          <AnswerStage
            key={pendingQuestions[0].callId}
            question={pendingQuestions[0]}
            busy={!!questionsBusy}
            onAnswer={onAnswerQuestion}
            onDismiss={onDismissQuestion ?? (() => {})}
          />
        ) : (
          <Composer
            draftKey={thread.id}
            threadId={thread.id && thread.id !== 'new' ? thread.id : undefined}
            onSend={onSend}
            onStartAutonomous={onStartAutonomous}
            scope={scope}
            setScope={setScope}
            mode={mode}
            setMode={setMode}
            duration={duration}
            setDuration={setDuration}
            streaming={streaming}
            agentStatus={agentStatus}
            agentIsAutonomous={agentIsAutonomous}
            agentElapsed={agentElapsed}
            agentRetry={agentRetry}
            onPauseAgent={onPauseAgent}
            onResumeAgent={onResumeAgent}
            onStopAgent={onStopAgent}
            agentControlsPending={agentControlsPending}
          />
        )}
      <StreamScrollbar scrollRef={streamRef} indicators={indicators} />
    </div>
  )
}
