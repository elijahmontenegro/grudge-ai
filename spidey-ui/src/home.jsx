function Home({ threads, onOpenThread, onQuickStart, onOpenPalette }) {
  const [draft, setDraft] = useState("");
  const visible = threads.filter(t => !t.parentId && !t.archived);
  const autonomousCount = visible.filter(t => (t.elapsed || "").startsWith("autonomous")).length;
  const runningCount = visible.filter(t => t.state === "running").length;

  return (
    <div className="home">
      <h1>Good afternoon, {USER.name.split(" ")[0]}.</h1>
      <div className="sub">
        {visible.length} threads
        {runningCount > 0 && <> · {runningCount} streaming</>}
        {autonomousCount > 0 && <> · {autonomousCount} autonomous</>}
      </div>

      <div className="quickstart">
        <textarea
          placeholder="Start a new thread…"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          rows={3}
        />
        <div className="quickstart-foot">
          <span className="seg" style={{ display: "inline-flex", border: "1px solid var(--rule)", borderRadius: 3, overflow: "hidden" }}>
            <button style={{ padding: "3px 9px", color: "var(--muted)", borderRight: "1px solid var(--rule)", fontSize: 10.5 }}>no dir</button>
            <button style={{ padding: "3px 9px", color: "var(--muted)", fontSize: 10.5 }}>+ mount dir</button>
          </span>
          <span style={{ flex: 1 }} />
          <span>{IS_MAC ? "⌘↵" : "Ctrl+↵"} starts thread</span>
          <button
            onClick={() => { if (draft.trim()) onQuickStart(draft.trim()); }}
            style={{ padding: "5px 14px", background: "var(--ink)", color: "var(--paper)", borderRadius: 3, fontFamily: "var(--mono)", fontSize: 11, letterSpacing: "0.04em", textTransform: "uppercase" }}
          >start</button>
        </div>
      </div>

      <div className="home-grid">
        <div className="home-section">
          <h2>
            <span>recent threads</span>
            <span style={{ color: "var(--muted)", cursor: "pointer" }} onClick={onOpenPalette}>{kbdLabel("K")}</span>
          </h2>
          {visible.slice(0, 6).map(t => (
            <CorpusCard key={t.id} thread={t} onOpen={() => onOpenThread(t.id)} />
          ))}
        </div>

        <div className="home-section">
          <h2><span>activity</span><span style={{ color: "var(--muted)" }}>live</span></h2>
          {ACTIVITY.map((a, i) => (
            <div className="activity-item" key={i}>
              <span className="when">{a.when}</span>
              <span className="what">{a.what}</span>
            </div>
          ))}
        </div>
      </div>

      <div style={{ marginTop: 56, fontFamily: "var(--mono)", fontSize: 10.5, color: "var(--muted)", letterSpacing: "0.04em", textTransform: "uppercase", borderTop: "1px solid var(--rule)", paddingTop: 14 }}>
        spidey — rrc-native · lossless corpus · no memory settings because they're redundant
      </div>
    </div>
  );
}

// Thread card. Minimal meta; no warmth. A tiny corpus sparkline hints at scale
// and recency without putting a number on it.
function CorpusCard({ thread, onOpen }) {
  const N = 40;
  const count = thread.msgCount || 0;
  // Seeded pseudo-random so the sparkline is stable per-thread (no hydration
  // flicker from Math.random on rerender).
  const seed = Array.from(thread.id).reduce((a, c) => a + c.charCodeAt(0), 0);
  const rand = (i) => {
    const x = Math.sin(seed * 91 + i * 17) * 10000;
    return x - Math.floor(x);
  };
  const bars = [];
  for (let i = 0; i < N; i++) {
    const frac = i / N;
    const present = frac < Math.min(1, count / 50);
    const role = present && rand(i) < 0.35 ? "user" : "assistant";
    bars.push(
      <i
        key={i}
        data-role={role}
        style={{ height: present ? `${6 + rand(i + 100) * 20}px` : "2px" }}
      />
    );
  }

  const autonomous = (thread.elapsed || "").startsWith("autonomous");
  return (
    <div className="corpus-card" onClick={onOpen}>
      <div className="corpus-glyph">{bars}</div>
      <div className="corpus-card-body">
        <div className="name">{thread.name}</div>
        <div className="meta">
          {thread.msgCount} messages
          {thread.lastActive && <> · active {thread.lastActive}</>}
          {autonomous && <> · <span style={{ color: "var(--accent-ink)" }}>{thread.elapsed}</span></>}
        </div>
      </div>
    </div>
  );
}

Object.assign(window, { Home });
