// Introspection drawer. Opens when a citation is clicked.
// Shows: score breakdown, QUD graph state, DAG neighborhood, exclusions.

function Drawer({ open, citation, selection, corpus, corpusMap, onClose }) {
  if (!open || !citation) return null;

  const msg = corpusMap[citation.id];
  const crossThread = citation.crossThread;

  return (
    <aside className="drawer">
      <div className="drawer-head">
        <span className="kicker">why</span>
        <span className="title">{crossThread ? "this pull" : "this prereq"}</span>
        <span className="spacer" />
        <button onClick={onClose} aria-label="Close"><IconX size={12} /></button>
      </div>
      <div className="drawer-body scroll">
        <div className="drawer-section">
          <h3><span className="n">01</span>Message</h3>
          <div style={{ fontFamily: "var(--mono)", fontSize: 10.5, color: "var(--muted)", letterSpacing: "0.04em", marginBottom: 10 }}>
            {crossThread ? (
              <>↗ <span style={{ color: "var(--accent-ink)" }}>{citation.threadName}</span> &nbsp;·&nbsp; cross-thread pull</>
            ) : (
              <>#{msg?.pos} &nbsp;·&nbsp; {msg?.role} &nbsp;·&nbsp; in-thread</>
            )}
          </div>
          <div style={{ fontFamily: "var(--serif)", fontSize: 14, lineHeight: 1.55, color: "var(--ink)" }}>
            {msg?.text || citation.snippet}
          </div>
        </div>

        <div className="drawer-section">
          <h3><span className="n">02</span>Score breakdown</h3>
          <ScoreBreakdown citation={citation} />
        </div>

        <div className="drawer-section">
          <h3><span className="n">03</span>QUD context</h3>
          <QUDGraphView quds={QUDS} highlightMsgId={citation.id} />
        </div>

        <div className="drawer-section">
          <h3><span className="n">04</span>DAG neighborhood</h3>
          <DAGNeighborhood msgId={citation.id} corpus={corpus} corpusMap={corpusMap} />
        </div>

        {selection?.excluded?.length > 0 && (
          <div className="drawer-section">
            <h3><span className="n">05</span>Also considered · excluded</h3>
            {selection.excluded.map(ex => {
              const m = corpusMap[ex.id];
              return (
                <div className="excl" key={ex.id}>
                  <span className="t"><span className="pos">#{m?.pos}</span>{m?.text?.slice(0, 92)}…</span>
                  <span className="r">{ex.reason.replace(/-/g, " ")}<span className="s">{fmtScore(ex.score)}</span></span>
                </div>
              );
            })}
          </div>
        )}
      </div>
    </aside>
  );
}

function ScoreBreakdown({ citation }) {
  const ce = citation.ce ?? 0;
  const qud = citation.qud ?? 0;
  const temp = citation.temp ?? 0;
  const w1 = 0.7, w2 = 0.2, w3 = 0.1;
  const fused = w1 * ce + w2 * qud + w3 * temp;

  const row = (k, label, weight, v) => (
    <div className="score-row">
      <span className="label">{label}<sup>w={weight}</sup></span>
      <span className="bar" data-k={k} data-empty={v === 0}>
        <i style={{ width: `${v * 100}%` }} />
      </span>
      <span className="val" data-empty={v === 0}>{fmtScore(v)}</span>
    </div>
  );

  return (
    <div className="score-block">
      {row("ce", "cross-enc", ".7", ce)}
      {row("qud", "qud edge", ".2", qud)}
      {row("temp", "temporal", ".1", temp)}
      <div className="score-formula">
        <span className="num">.7 × {fmtScore(ce)}</span> + <span className="num">.2 × {fmtScore(qud)}</span> + <span className="num">.1 × {fmtScore(temp)}</span> &nbsp;=&nbsp; <span className="result">{fmtScore(fused)}</span>
        {citation.hop > 1 && (
          <span className="decay">
            ↳ after {citation.hop - 1}-hop decay ({Math.pow(0.8, citation.hop - 1).toFixed(2)}×) → <span className="result">{fmtScore(citation.score)}</span>
          </span>
        )}
      </div>
    </div>
  );
}

function QUDGraphView({ quds, highlightMsgId }) {
  const byParent = { null: [] };
  quds.forEach(q => (byParent[q.parent] ||= []).push(q));

  function render(parentId, depth) {
    return (byParent[parentId] || []).map(q => {
      const highlighted = q.addressedBy.includes(highlightMsgId) || q.by === highlightMsgId;
      return (
        <React.Fragment key={q.id}>
          <div
            className="qud"
            data-status={q.status}
            data-child={depth > 0}
            data-highlighted={highlighted}
            style={{ marginLeft: depth * 20 }}
          >
            <span className="q-status">{q.status}</span>
            <span className="q-id">{q.id}</span>
            <span className="q-text">{q.q}</span>
            <span className="q-meta">
              est. {q.by}
              {q.addressedBy.length > 0 && <> &nbsp;·&nbsp; addressed {q.addressedBy.join(", ")}</>}
            </span>
          </div>
          {render(q.id, depth + 1)}
        </React.Fragment>
      );
    });
  }

  return <div className="qud-graph">{render(null, 0)}</div>;
}

function DAGNeighborhood({ msgId, corpus, corpusMap }) {
  const target = corpusMap[msgId];
  if (!target) return <div style={{ color: "var(--muted)", fontSize: 12 }}>No DAG neighborhood available.</div>;

  const earlier = corpus.filter(m => m.pos < target.pos).slice(-3);
  const later = corpus.filter(m => m.pos > target.pos).slice(0, 3);
  const scores = { prereq: [0.87, 0.64, 0.52], dep: [0.79, 0.61, 0.48] };

  return (
    <div className="dag-view">
      {earlier.length > 0 && (
        <>
          <div className="dag-group-label">↑ prereqs · what this builds on</div>
          <div className="dag-group">
            {earlier.map((m, i) => (
              <div key={m.id} className="dag-node" data-kind="prereq">
                <span className="glyph" />
                <span className="label">#{m.pos}</span>
                <span className="snippet">{m.text}</span>
                <span className="edge-score">{fmtScore(scores.prereq[earlier.length - 1 - i])}</span>
              </div>
            ))}
          </div>
        </>
      )}

      <div className="dag-node" data-current="true">
        <span className="glyph" />
        <span className="label">#{target.pos}</span>
        <span className="snippet">{target.text}</span>
        <span className="edge-score">current</span>
      </div>

      {later.length > 0 && (
        <>
          <div className="dag-group-label">↓ dependents · what builds on this</div>
          <div className="dag-group">
            {later.map((m, i) => (
              <div key={m.id} className="dag-node" data-kind="dep">
                <span className="glyph" />
                <span className="label">#{m.pos}</span>
                <span className="snippet">{m.text}</span>
                <span className="edge-score">{fmtScore(scores.dep[i])}</span>
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  );
}

Object.assign(window, { Drawer });
