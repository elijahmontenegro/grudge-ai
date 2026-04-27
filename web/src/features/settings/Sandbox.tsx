/**
 * Per-thread sandbox flags live in ThreadConfigPopover (per-thread
 * gear). This section is reserved for sandbox-wide settings — image
 * version, host bind defaults, resource limits — none of which are
 * wired through the settings schema yet. The placeholder keeps the
 * structure aligned with the plan's six sections without inventing
 * new state.
 */
export function Sandbox() {
  return (
    <div className="role-card">
      <h3>Sandbox</h3>
      <div className="role-name">
        Sandbox toggles live on individual threads (gear icon in the topbar).
        Sandbox-wide settings — image version, resource caps — will land here.
      </div>
    </div>
  )
}
