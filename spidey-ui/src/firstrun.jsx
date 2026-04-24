function FirstRun({ onComplete }) {
  const [name, setName] = useState("John Doe");
  return (
    <div className="firstrun">
      <div className="firstrun-brand">
        <span className="firstrun-dot" />
        <span className="firstrun-wordmark">spidey</span>
        <span className="firstrun-sep">/</span>
        <span className="firstrun-crumb">first run</span>
      </div>
      <h1>Welcome to Spidey.</h1>
      <div className="lede">
        Spidey runs locally. All three model roles need a provider.
        Your name stays in the system prompt — it's not a memory feature.
      </div>

      <div className="role-card">
        <h3>You</h3>
        <div className="role-name">Profile</div>
        <div className="row">
          <label>Name</label>
          <input value={name} onChange={(e) => setName(e.target.value)} />
        </div>
      </div>

      <div className="role-card">
        <h3>Main model · your LLM</h3>
        <div className="role-name">Reasoning, tool use, responses</div>
        <div className="detected">● <b>detected</b> · Ollama at localhost:11434</div>
        <div className="row">
          <label>adapter</label>
          <input defaultValue="ollama" />
        </div>
        <div className="row">
          <label>model</label>
          <input defaultValue="llama3.1:70b-instruct" />
        </div>
      </div>

      <div className="role-card">
        <h3>Cross-encoder · rrc signal</h3>
        <div className="role-name">Pairwise dependency scoring</div>
        <div className="detected">● <b>detected</b> · TEI at localhost:8080</div>
        <div className="row">
          <label>model</label>
          <input defaultValue="cross-encoder/nli-deberta-v3-base" />
        </div>
      </div>

      <div className="role-card">
        <h3>Small fast model · qud extraction</h3>
        <div className="role-name">Sub-second inference required</div>
        <div className="missing">○ <b>not detected</b> · configure manually</div>
        <div className="row">
          <label>adapter</label>
          <input placeholder="ollama / openai / anthropic" />
        </div>
        <div className="row">
          <label>model</label>
          <input placeholder="osmosis-structure-0.6b" />
        </div>
      </div>

      <div style={{ display: "flex", gap: 12, marginTop: 24 }}>
        <button
          onClick={onComplete}
          style={{ padding: "9px 18px", background: "var(--ink)", color: "var(--paper)", borderRadius: 3, fontFamily: "var(--mono)", fontSize: 12, letterSpacing: "0.04em", textTransform: "uppercase" }}
        >
          confirm · open spidey
        </button>
        <div style={{ flex: 1 }} />
        <div style={{ fontFamily: "var(--mono)", fontSize: 11, color: "var(--muted)", alignSelf: "center" }}>
          no cloud · no multi-tenant · no degraded mode
        </div>
      </div>
    </div>
  );
}

Object.assign(window, { FirstRun });
