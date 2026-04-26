import { useEffect, useRef, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router'
import { ThreadView } from '@/components/organisms/ThreadView'
import type {
  AutonomousDuration,
  ComposerMode,
  ComposerScope,
} from '@/components/organisms/Composer'
import { useThreadMessages } from '@/hooks/useThreadMessages'
import { useThreads } from '@/hooks/useThreads'
import { useSendAndStream } from '@/hooks/useSendAndStream'
import { useAgentState } from '@/hooks/useAgentState'
import { useSubagentProgress } from '@/hooks/useSubagentProgress'
import { usePlanMode } from '@/hooks/usePlanMode'
import { useStartAutonomous } from '@/hooks/useStartAutonomous'
import { useToolExecutions } from '@/hooks/useToolExecutions'
import { useEditMessage } from '@/hooks/useEditMessage'
import { useAgentControls } from '@/hooks/useAgentControls'
import { useCreateThread } from '@/hooks/useCreateThread'
import { AgentMode, AgentStatus } from '@/graphql/generated/types'
interface ThreadPageProps {
  mode: ComposerMode
  setMode: (m: ComposerMode) => void
  scope: ComposerScope
  setScope: (s: ComposerScope) => void
  duration: AutonomousDuration
  setDuration: (d: AutonomousDuration) => void
  artifactsCollapsed: boolean
  onToggleArtifacts: () => void
}

export function ThreadPage({
  mode,
  setMode,
  scope,
  setScope,
  duration,
  setDuration,
  artifactsCollapsed,
  onToggleArtifacts,
}: ThreadPageProps) {
  const { id: rawId = '' } = useParams<{ id: string }>()
  // "/thread/new" is a DRAFT — no DB row yet. The thread is materialised
  // on first send so clicking New doesn't litter the DB with empty rows
  // the user never actually used. All id-keyed hooks get an empty id
  // while drafting so their subscriptions/queries skip.
  const isDraft = rawId === 'new'
  const id = isDraft ? '' : rawId
  const navigate = useNavigate()
  const location = useLocation() as {
    state?: {
      initialMessage?: string
      focusMessageId?: string
      startAutonomous?: { text: string; duration: AutonomousDuration }
    }
  }
  const initialMessage = location.state?.initialMessage
  const focusMessageId = location.state?.focusMessageId
  const startAutonomousOnMount = location.state?.startAutonomous
  const { thread, messages, loading, error } = useThreadMessages(id)
  const { threads: allThreads } = useThreads()
  const agent = useAgentState(id)
  // When the agent is running server-side (autonomous, another tab's send,
  // subagent work) we want the messageStream subscription open even though
  // this tab didn't initiate. `externalStreaming` signals that.
  const agentRunning = agent.status === AgentStatus.Running
  const { send, streaming, stream } = useSendAndStream(id, scope, {
    externalStreaming: agentRunning,
  })
  const { create: createThread } = useCreateThread()

  // Reset key bumped each time streaming transitions false→true so live
  // tool-call and subagent-progress lists start fresh for the new turn
  // rather than stacking on top of the prior one.
  const [turnSeq, setTurnSeq] = useState(0)
  const prevStreamingRef = useRef(false)
  useEffect(() => {
    if (streaming && !prevStreamingRef.current) setTurnSeq((n) => n + 1)
    prevStreamingRef.current = streaming
  }, [streaming])

  const subagents = useSubagentProgress(id, turnSeq)
  const plan = usePlanMode()
  const autonomous = useStartAutonomous()
  // Single TOOL_EXECUTION subscription for this thread — exposes pending
  // approvals, AskUserQuestion prompts, and a live-call list all from one
  // stream (previously two separate subscriptions for the same events).
  const tools = useToolExecutions(id, turnSeq)
  const liveTools = tools.live
  const approvals = tools
  const editMsg = useEditMessage()
  const agentControls = useAgentControls()

  // Shared edit handler for every Turn in the thread. Navigates to the new
  // branched thread the backend returns.
  const onEditTurn = async (position: number, newContent: string) => {
    const newId = await editMsg.edit(id, position, newContent)
    if (newId && newId !== id) navigate(`/thread/${newId}`)
    return newId
  }

  // Auto-send an initial message when navigated here with router state
  // (e.g. Home quickstart or a draft → real-thread transition). Guard
  // so it only fires once per mount, and replace history immediately so
  // a page refresh doesn't resend the same message on remount (browser
  // persists navigation state across reloads).
  const autoSentRef = useRef(false)
  useEffect(() => {
    if (autoSentRef.current || !id) return
    if (initialMessage) {
      autoSentRef.current = true
      navigate(`/thread/${id}`, { replace: true, state: null })
      // Respect the caller's active mode: if they picked plan, enter
      // plan before the send so the runner sees the plan-mode prompt.
      // Autonomous is handled by its own branch below.
      ;(async () => {
        if (mode === 'plan' && agent.mode !== AgentMode.Plan) {
          await plan.enterPlan(id)
        }
        void send(initialMessage)
      })()
      return
    }
    if (startAutonomousOnMount) {
      autoSentRef.current = true
      navigate(`/thread/${id}`, { replace: true, state: null })
      void autonomous.start(id, startAutonomousOnMount.text, startAutonomousOnMount.duration)
      return
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialMessage, startAutonomousOnMount, id])

  // Plan mode is committed only at send time — flipping the pill is pure
  // UI intent, not a backend state change. This mirrors claude-code's
  // model where plan mode is a permission flag the user triggers via
  // /plan (a deliberate action), not a passive toggle. Calling
  // enterPlanMode on pill flip previously made the agent look "running"
  // (see round 90) and left no symmetric exit if the user flipped back.

  // Keep composer mode in sync with the backend's agent mode on server-
  // initiated transitions. We only act on real transitions OUT of Plan or
  // Autonomous, not on every observation of Normal — otherwise the moment
  // the user flips the pill to 'plan' (before enterPlan's mutation lands),
  // this effect fires with agent.mode still Normal and snaps mode back,
  // silently reverting the user's selection.
  const prevAgentModeRef = useRef(agent.mode)
  useEffect(() => {
    const prev = prevAgentModeRef.current
    prevAgentModeRef.current = agent.mode
    if (prev === agent.mode) return
    if (agent.mode === AgentMode.Normal) {
      if (prev === AgentMode.Plan && mode === 'plan') setMode('normal')
      else if (prev === AgentMode.Autonomous && mode === 'autonomous') setMode('normal')
    } else if (agent.mode === AgentMode.Autonomous) {
      if (mode !== 'autonomous') setMode('autonomous')
    }
  }, [agent.mode, mode, setMode])

  // Draft thread: no DB row, render a minimal shell with the composer
  // active. Hitting send / start-autonomous actually creates the row
  // and navigates to the real thread URL — until then, closing the tab
  // leaves nothing behind.
  if (isDraft) {
    const placeholderThread = {
      id: 'new',
      name: 'New thread',
      state: 'idle' as const,
      lastActive: '—',
      msgCount: 0,
      workingDirs: [] as string[],
      sandboxed: true,
      corpus: [] as typeof messages,
    }
    return (
      <ThreadView
        thread={placeholderThread}
        parentName={null}
        streaming={false}
        stream={stream}
        subagents={[]}
        liveTools={[]}
        onSend={async (text) => {
          const newId = await createThread()
          if (!newId) return
          navigate(`/thread/${newId}`, { state: { initialMessage: text } })
        }}
        onStartAutonomous={async (text, duration) => {
          const newId = await createThread()
          if (!newId) return
          navigate(`/thread/${newId}`, {
            state: { startAutonomous: { text, duration } },
          })
        }}
        scope={scope}
        setScope={setScope}
        mode={mode}
        setMode={setMode}
        duration={duration}
        setDuration={setDuration}
        focusMessageId={undefined}
        onEditTurn={undefined}
        editInFlight={false}
        artifactsCollapsed={artifactsCollapsed}
        onToggleArtifacts={onToggleArtifacts}
        livePlanContent={null}
        agentMode={AgentMode.Normal}
        planApproving={false}
        planRejecting={false}
        planEditing={false}
        onApprovePlan={() => {}}
        onRejectPlan={() => {}}
        onEditPlan={() => {}}
        agentStatus={AgentStatus.Idle}
        agentIsAutonomous={false}
        agentElapsed={null}
        onPauseAgent={() => {}}
        onResumeAgent={() => {}}
        onStopAgent={() => {}}
        agentControlsPending={false}
      />
    )
  }

  if (loading && !thread) {
    return (
      <div className="empty-thread">
        <p className="empty-thread-hint">loading thread…</p>
      </div>
    )
  }

  if (error) {
    return (
      <div className="empty-thread">
        <div className="empty-thread-rule">
          <span>backend unreachable</span>
        </div>
        <p className="empty-thread-lede" style={{ color: 'var(--danger)' }}>
          {error}
        </p>
        <p className="empty-thread-hint">
          Start the Spidey service — it exposes /graphql at the same host that serves this app.
        </p>
      </div>
    )
  }

  if (!thread) {
    return (
      <div className="empty-thread">
        <div className="empty-thread-rule">
          <span>thread not found</span>
        </div>
        <p className="empty-thread-lede">
          No thread with id <code>{id}</code>. It may have been deleted.
        </p>
      </div>
    )
  }

  const parent = thread.parentId ? allThreads.find((t) => t.id === thread.parentId) : null
  const fullThread = { ...thread, corpus: messages }

  return (
    <ThreadView
      thread={fullThread}
      parentName={parent?.name}
      streaming={streaming}
      stream={stream}
      subagents={subagents}
      liveTools={liveTools}
      onSend={async (text, attachments) => {
        // Commit plan mode at send time if the user selected it. Runner
        // restart inside enterPlanMode rebuilds the system prompt before
        // the next SendMessage sees it.
        if (mode === 'plan' && agent.mode !== AgentMode.Plan) {
          await plan.enterPlan(id)
        }
        void send(text, attachments)
      }}
      onStartAutonomous={(text, d, attachments) => {
        void autonomous.start(id, text, d, attachments)
      }}
      scope={scope}
      setScope={setScope}
      mode={mode}
      setMode={setMode}
      duration={duration}
      setDuration={setDuration}
      focusMessageId={focusMessageId}
      onEditTurn={onEditTurn}
      editInFlight={editMsg.loading}
      artifactsCollapsed={artifactsCollapsed}
      onToggleArtifacts={onToggleArtifacts}
      livePlanContent={agent.planContent}
      agentMode={agent.mode}
      planApproving={plan.approving}
      planRejecting={plan.rejecting}
      planEditing={plan.editing}
      onApprovePlan={(auto) => void plan.approvePlan(id, auto)}
      onRejectPlan={(feedback) => void plan.rejectPlan(id, feedback)}
      onEditPlan={(content) => plan.editPlan(id, content)}
      agentStatus={agent.status}
      agentIsAutonomous={agent.isAutonomous}
      agentElapsed={agent.elapsedTime}
      agentRetry={agent.retry}
      onPauseAgent={() => void agentControls.pause(id)}
      onResumeAgent={(correction) => void agentControls.resume(id, correction)}
      onStopAgent={() => void agentControls.stop(id)}
      agentControlsPending={agentControls.pending}
      pendingQuestions={approvals.questions}
      onAnswerQuestion={(cid, a) => approvals.answer(cid, a)}
      onDismissQuestion={(cid) => approvals.dismissQuestion(cid)}
      questionsBusy={approvals.busy}
      pendingApprovals={approvals.pending}
      onApproveTool={(cid) => void approvals.approve(cid)}
      onDenyTool={(cid, r) => void approvals.deny(cid, r)}
      approvalsBusy={approvals.busy}
    />
  )
}
