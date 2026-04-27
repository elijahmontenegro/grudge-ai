import { useEffect, useRef, useState } from 'react'
import { IconPause, IconPlay, IconStop } from '@/primitives/icons'
import type { AgentRetryStatus } from './types'

interface StatusProps {
  visible: boolean
  paused: boolean
  running: boolean
  isAutonomous: boolean
  elapsed: string | null
  retry: AgentRetryStatus | null
  hasPendingResumeText: boolean
  pending: boolean
  onPause?: () => void
  onResume?: (correction?: string) => void
  onStop?: () => void
}

/** The retry strip + runtime status row (running/paused chip with
 *  pause/resume/stop). Visible during any in-flight work — autonomous
 *  runs AND normal streaming turns. Stop belongs on every turn the
 *  user might want to abort; pause stays autonomous-only. */
export function Status({
  visible,
  paused,
  running,
  isAutonomous,
  elapsed,
  retry,
  hasPendingResumeText,
  pending,
  onPause,
  onResume,
  onStop,
}: StatusProps) {
  const label = paused
    ? 'paused'
    : isAutonomous
      ? `autonomous${elapsed ? ` · ${elapsed}` : ''}`
      : running
        ? `running${elapsed ? ` · ${elapsed}` : ''}`
        : 'working'

  return (
    <>
      {retry && (
        <div
          className="composer-retry"
          data-final={retry.final && retry.error ? 'true' : undefined}
          role="status"
          aria-live="polite"
        >
          <span className="composer-retry-kicker">
            {retry.final && retry.error ? 'retry exhausted' : 'retrying'}
          </span>
          <span className="composer-retry-progress">
            {retry.attempt}/{retry.maxAttempts}
          </span>
          {!retry.final && retry.nextDelayMs > 0 && (
            <RetryCountdown nextDelayMs={retry.nextDelayMs} />
          )}
          {retry.error && (
            <span className="composer-retry-error" title={retry.error}>
              {retry.error.length > 80 ? retry.error.slice(0, 80) + '…' : retry.error}
            </span>
          )}
        </div>
      )}
      {visible && (
        <div
          className="composer-status"
          data-state={paused ? 'paused' : running ? 'running' : 'working'}
        >
          <span className="composer-status-tick" aria-hidden="true" />
          <span className="composer-status-label">{label}</span>
          <span style={{ flex: 1 }} />
          {running && onPause && (
            <button
              className="composer-status-btn"
              onClick={onPause}
              disabled={pending}
              title="Pause"
            >
              <IconPause size={10} />
              <span>pause</span>
            </button>
          )}
          {paused && onResume && !hasPendingResumeText && (
            <button
              className="composer-status-btn"
              onClick={() => onResume(undefined)}
              disabled={pending}
              title="Resume (no correction)"
            >
              <IconPlay size={10} />
              <span>resume</span>
            </button>
          )}
          {onStop && (
            <button
              className="composer-status-btn danger"
              onClick={onStop}
              disabled={pending}
              title="Stop"
            >
              <IconStop size={10} />
              <span>stop</span>
            </button>
          )}
        </div>
      )}
    </>
  )
}

// RetryCountdown renders a live wall-clock countdown of remaining
// time until the next retry attempt. The backend sends nextDelayMs
// once, at the moment the retry is scheduled — that number is a
// frozen snapshot. Without a client-side timer the UI would show
// "next in 58s" for the whole wait and never tick. We record the
// arrival time and re-render once per second against the wall clock.
// Resets when `nextDelayMs` changes (a fresh retry event arrived).
function RetryCountdown({ nextDelayMs }: { nextDelayMs: number }) {
  const arrivedRef = useRef(Date.now())
  const [, forceTick] = useState(0)

  useEffect(() => {
    arrivedRef.current = Date.now()
    forceTick((n) => n + 1)
  }, [nextDelayMs])

  useEffect(() => {
    const id = window.setInterval(() => forceTick((n) => n + 1), 1000)
    return () => window.clearInterval(id)
  }, [])

  const elapsed = Date.now() - arrivedRef.current
  const remainingMs = Math.max(0, nextDelayMs - elapsed)
  const secs = Math.ceil(remainingMs / 1000)
  return <span className="composer-retry-wait">next in {secs}s</span>
}
