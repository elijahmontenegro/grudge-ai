import { useEffect, useState } from 'react'
import { useLocation } from 'react-router'
import {
  Composer,
  type AutonomousDuration,
  type ComposerMode,
  type ComposerScope,
} from '@/features/composer/Composer'
import { useMe, useGreeting } from '@/hooks/useMe'

interface HomeProps {
  /** Called when the user submits a plain or plan-mode turn. Receives
   *  the trimmed text, the working directories collected in the
   *  mount-dir row, and the sandbox toggle state. The caller creates
   *  the thread and navigates. */
  onQuickStart: (text: string, workingDirs: string[], sandboxed: boolean) => void
  /** Called when the user starts an autonomous run from Home. */
  onStartAutonomous: (
    text: string,
    duration: AutonomousDuration,
    workingDirs: string[],
    sandboxed: boolean,
  ) => void
  // Composer state is lifted to App so it persists across navigation
  // (mode/scope/duration survive hopping between home/threads) and the
  // CommandPalette can toggle it. Same props Thread gets.
  mode: ComposerMode
  setMode: (m: ComposerMode) => void
  scope: ComposerScope
  setScope: (s: ComposerScope) => void
  duration: AutonomousDuration
  setDuration: (d: AutonomousDuration) => void
}

/**
 * Home is the composer-first landing. The composer itself is the shared
 * Composer organism — same component Thread uses. Home layers two things
 * on top: a greeting and the working-dirs row (mount-dir state is Home-
 * owned because it's per-new-thread config, not per-message).
 */
export function Home({
  onQuickStart,
  onStartAutonomous,
  mode,
  setMode,
  scope,
  setScope,
  duration,
  setDuration,
}: HomeProps) {
  const [workingDirs, setWorkingDirs] = useState<string[]>([])
  // Sandbox default matches backend default (true). Users who want host
  // access for a specific project toggle it off before sending.
  const [sandboxed, setSandboxed] = useState<boolean>(true)

  const me = useMe()
  const greeting = useGreeting()

  // Clicking "New" in the sidebar navigates here with a `freshAt`
  // timestamp in route state. When that value changes we reset the
  // working-dirs list and bump an animation key so the composer replays
  // its entrance — gives clear feedback for "start a new thread" even
  // when the user is already on /. Composer's draft is keyed on the
  // `home` draftKey and cleared by the Composer's own send path.
  const location = useLocation() as { state?: { freshAt?: number } }
  const freshAt = location.state?.freshAt
  useEffect(() => {
    if (freshAt === undefined) return
    setWorkingDirs([])
    setSandboxed(true)
  }, [freshAt])

  return (
    <div
      className="home"
      key={freshAt ?? 'initial'}
      data-fresh={freshAt !== undefined || undefined}
    >
      <h1>
        {greeting}, {me.name.split(' ')[0]}.
      </h1>

      {/* Banner slot — future home of announcements, changelog nudges,
          setup reminders. Empty by default. Render nothing until we
          actually have something to say; no placeholder copy. */}

      <Composer
        draftKey="home"
        onSend={(text) => onQuickStart(text, workingDirs, sandboxed)}
        onStartAutonomous={(text, d) => onStartAutonomous(text, d, workingDirs, sandboxed)}
        mode={mode}
        setMode={setMode}
        scope={scope}
        setScope={setScope}
        duration={duration}
        setDuration={setDuration}
        streaming={false}
        workingDirs={workingDirs}
        onWorkingDirsChange={setWorkingDirs}
        sandboxed={sandboxed}
        onSandboxedChange={setSandboxed}
      />
    </div>
  )
}
