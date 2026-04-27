// Build constants are injected by vite.config.ts via the define plugin.
declare const __BUILD_COMMIT__: string
declare const __BUILD_DATE__: string

export function About() {
  return (
    <div className="role-card">
      <h3>About</h3>
      <div className="role-name">Spidey — RRC-native agentic framework.</div>
      <div style={{ fontFamily: 'var(--mono)', fontSize: 11, marginTop: 8, opacity: 0.7 }}>
        build {__BUILD_COMMIT__} · {__BUILD_DATE__}
      </div>
    </div>
  )
}
