import { useState } from 'react'
import { gql } from '@apollo/client'
import { useMutation } from '@apollo/client/react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
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
  const { state } = useAgentState(threadId)

  const [startAutonomous] = useMutation<{startAutonomous: boolean}>(START_AUTONOMOUS)
  const [pauseAgent] = useMutation<{pauseAgent: boolean}>(PAUSE_AGENT)
  const [resumeAgent] = useMutation<{resumeAgent: boolean}>(RESUME_AGENT)
  const [stopAgent] = useMutation<{stopAgent: boolean}>(STOP_AGENT)

  const isRunning = state?.status === 'RUNNING'
  const isPaused = state?.status === 'PAUSED'
  const isAutonomous = state?.mode === 'AUTONOMOUS'

  if (isAutonomous && (isRunning || isPaused)) {
    return (
      <div className="border border-border rounded-lg p-3 space-y-2 bg-card">
        <div className="flex items-center gap-2">
          <Badge variant={isRunning ? 'default' : 'secondary'}>
            {state?.status}
          </Badge>
          <Badge variant="outline">Round {state?.roundCount}</Badge>
          {state?.elapsedTime && (
            <span className="text-xs text-muted">{state.elapsedTime}</span>
          )}
        </div>

        <div className="flex gap-2">
          {isRunning && (
            <Button size="sm" variant="outline" onClick={() => pauseAgent({ variables: { threadId } })}>
              Pause
            </Button>
          )}
          {isPaused && (
            <>
              <Input
                value={correction}
                onChange={(e) => setCorrection(e.target.value)}
                placeholder="Course correction (optional)"
                className="flex-1 text-sm"
              />
              <Button size="sm" onClick={() => {
                resumeAgent({ variables: { threadId, correction: correction || null } })
                setCorrection('')
              }}>
                Resume
              </Button>
            </>
          )}
          <Button size="sm" variant="destructive" onClick={() => stopAgent({ variables: { threadId } })}>
            Stop
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div className="border border-border rounded-lg p-3 space-y-2 bg-card">
      <h3 className="text-sm font-medium">Autonomous Mode</h3>
      <Input
        value={prompt}
        onChange={(e) => setPrompt(e.target.value)}
        placeholder="What should Spidey work on?"
      />
      <div className="flex gap-2 items-center">
        <Input
          value={duration}
          onChange={(e) => setDuration(e.target.value)}
          placeholder="Duration (e.g. 1h, 30m)"
          className="w-32"
        />
        <Button
          size="sm"
          disabled={!prompt.trim()}
          onClick={() => startAutonomous({
            variables: { threadId, prompt, duration },
          })}
        >
          Start
        </Button>
      </div>
    </div>
  )
}
