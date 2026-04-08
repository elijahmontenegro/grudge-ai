import { useState } from 'react'
import { gql } from '@apollo/client'
import { useMutation } from '@apollo/client/react'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'
import { useAgentState } from '@/hooks/useAgentState'

const START_AUTONOMOUS = gql`
  mutation StartAutonomous($threadId: ID!, $prompt: String!, $duration: String!) {
    startAutonomous(threadId: $threadId, prompt: $prompt, duration: $duration)
  }
`

const PAUSE_AGENT = gql`
  mutation PauseAgent($threadId: ID!) { pauseAgent(threadId: $threadId) }
`

const RESUME_AGENT = gql`
  mutation ResumeAgent($threadId: ID!, $correction: String) { resumeAgent(threadId: $threadId, correction: $correction) }
`

const STOP_AGENT = gql`
  mutation StopAgent($threadId: ID!) { stopAgent(threadId: $threadId) }
`

interface Props {
  threadId: string
}

export function AutonomousControls({ threadId }: Props) {
  const [prompt, setPrompt] = useState('')
  const [duration, setDuration] = useState('1h')
  const [correction, setCorrection] = useState('')
  const [expanded, setExpanded] = useState(false)
  const { state } = useAgentState(threadId)

  const [startAutonomous] = useMutation<{startAutonomous: boolean}>(START_AUTONOMOUS)
  const [pauseAgent] = useMutation<{pauseAgent: boolean}>(PAUSE_AGENT)
  const [resumeAgent] = useMutation<{resumeAgent: boolean}>(RESUME_AGENT)
  const [stopAgent] = useMutation<{stopAgent: boolean}>(STOP_AGENT)

  const isRunning = state?.status === 'RUNNING'
  const isPaused = state?.status === 'PAUSED'
  const isAutonomous = state?.mode === 'AUTONOMOUS'

  // Running state — prominent status bar
  if (isAutonomous && (isRunning || isPaused)) {
    return (
      <div className="rounded-xl bg-card p-4 mb-3 animate-fade-in">
        <div className="flex items-center gap-3 mb-3">
          <span className={cn(
            'h-2 w-2 rounded-full shrink-0',
            isRunning ? 'bg-emerald-500 animate-pulse-subtle' : 'bg-amber-500'
          )} />
          <span className="text-sm font-medium">{isRunning ? 'Autonomous' : 'Paused'}</span>
          <span className="text-xs text-muted-foreground/50">Round {state?.roundCount ?? 0}</span>
          {state?.elapsedTime && (
            <span className="text-xs text-muted-foreground/40 tabular-nums">{state.elapsedTime}</span>
          )}
        </div>

        {isPaused && (
          <div className="flex items-center gap-2 mb-3">
            <Input
              value={correction}
              onChange={(e) => setCorrection(e.target.value)}
              placeholder="Course correction (optional)..."
              className="flex-1 h-8 text-sm"
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  resumeAgent({ variables: { threadId, correction: correction || null } })
                  setCorrection('')
                }
              }}
            />
          </div>
        )}

        <div className="flex items-center gap-2">
          {isRunning && (
            <button
              onClick={() => pauseAgent({ variables: { threadId } })}
              className="px-3 py-1.5 rounded-lg text-xs font-medium bg-foreground/[0.05] hover:bg-foreground/[0.08] transition-colors"
            >
              Pause
            </button>
          )}
          {isPaused && (
            <button
              onClick={() => {
                resumeAgent({ variables: { threadId, correction: correction || null } })
                setCorrection('')
              }}
              className="px-3 py-1.5 rounded-lg text-xs font-medium bg-primary/10 text-primary hover:bg-primary/20 transition-colors"
            >
              Resume
            </button>
          )}
          <button
            onClick={() => stopAgent({ variables: { threadId } })}
            className="px-3 py-1.5 rounded-lg text-xs font-medium text-destructive hover:bg-destructive/10 transition-colors"
          >
            Stop
          </button>
        </div>
      </div>
    )
  }

  // Setup form
  if (expanded) {
    return (
      <div className="rounded-xl bg-card p-4 mb-3 animate-fade-in">
        <div className="text-sm font-medium mb-1">Autonomous mode</div>
        <p className="text-xs text-muted-foreground/40 mb-3">
          The agent works independently on a task. RRC scores every round.
        </p>
        <div className="space-y-2">
          <Input
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && prompt.trim()) {
                startAutonomous({ variables: { threadId, prompt, duration } })
                setExpanded(false)
              }
              if (e.key === 'Escape') setExpanded(false)
            }}
            placeholder="What should Spidey work on?"
            className="text-sm"
            autoFocus
          />
          <div className="flex items-center gap-2">
            <Input
              value={duration}
              onChange={(e) => setDuration(e.target.value)}
              placeholder="1h"
              className="w-20 text-sm text-center"
            />
            <button
              disabled={!prompt.trim()}
              onClick={() => {
                startAutonomous({ variables: { threadId, prompt, duration } })
                setExpanded(false)
              }}
              className={cn(
                'px-4 py-1.5 rounded-lg text-xs font-medium transition-colors',
                prompt.trim()
                  ? 'bg-primary text-primary-foreground hover:bg-primary/90'
                  : 'bg-foreground/[0.05] text-muted-foreground/30'
              )}
            >
              Start
            </button>
            <button
              onClick={() => setExpanded(false)}
              className="px-3 py-1.5 rounded-lg text-xs text-muted-foreground/50 hover:text-muted-foreground transition-colors"
            >
              Cancel
            </button>
          </div>
        </div>
      </div>
    )
  }

  // Trigger — proper button with icon and description, not a text link
  return (
    <button
      onClick={() => setExpanded(true)}
      className="flex items-center gap-3 px-3 py-2 rounded-lg hover:bg-foreground/[0.03] transition-colors text-left group"
    >
      <span className="h-7 w-7 rounded-lg bg-emerald-500/10 flex items-center justify-center text-emerald-500 shrink-0 group-hover:bg-emerald-500/20 transition-colors">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><polygon points="5 3 19 12 5 21 5 3"/></svg>
      </span>
      <div>
        <div className="text-xs font-medium text-muted-foreground group-hover:text-foreground transition-colors">Autonomous</div>
        <div className="text-[10px] text-muted-foreground/30">Agent works independently</div>
      </div>
    </button>
  )
}
