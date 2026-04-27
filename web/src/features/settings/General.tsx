import type { Preferences } from './types'

interface GeneralProps {
  prefs: Preferences
  setPrefs: (next: Preferences) => void
}

/** Profile / preference settings — the user's name in system
 *  prompts, and any future user-facing toggles. */
export function General({ prefs, setPrefs }: GeneralProps) {
  return (
    <div className="role-card">
      <h3>Profile</h3>
      <div className="role-name">Name in system prompt</div>
      <div className="row">
        <label>name</label>
        <input
          value={prefs.name ?? ''}
          onChange={(e) => setPrefs({ ...prefs, name: e.target.value })}
          placeholder="Your name"
        />
      </div>
    </div>
  )
}
