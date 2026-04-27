/**
 * Build / version info. The frontend has no build-stamp wiring yet
 * (Vite could inject one via define plugin); the placeholder keeps
 * the structure aligned with the plan's six sections.
 */
export function About() {
  return (
    <div className="role-card">
      <h3>About</h3>
      <div className="role-name">
        Spidey · RRC-native agentic framework. Build / version info will land
        here once the frontend stamps a release marker.
      </div>
    </div>
  )
}
