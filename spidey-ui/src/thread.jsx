// Thread = an immutable CORPUS. Each turn renders its SELECTION (what RRC
// restored). This is a DOCUMENT surface, not a chat log. Read it top-to-bottom.
//
// Every turn is the same shape: prompt · marginalia · response. Marginalia
// is a narrow left gutter with numbered prereq markers ¹ ² ³ — click to open
// the introspection drawer. No collapse/expand, no per-turn chrome, no pills,
// no view-mode toggle. The one view IS selections; that's the whole point.
//
// Plan-mode turns have a distinct response shape: structured steps with
// approve/reject affordances.

// --- corpus rail (right edge minimap) ------------------------------------
// IDE-style minimap. Each tick is a corpus message at its proportional
// vertical position. A translucent band tracks what's currently visible in
// the scroll viewport. Selected ticks brighten — the "match highlight"
// that mirrors Cmd-F indicators. Clicking jumps.
function CorpusRail({ corpus, selectedIds, currentPos, onJump, scrollRef }) {
  const [range, setRange] = useState({ top: 0, height: 0.25 }); // fractional
  const railRef = useRef(null);
  const trackRef = useRef(null);

  useEffect(() => {
    const el = scrollRef?.current;
    if (!el) return;
    function update() {
      const full = el.scrollHeight || 1;
      const view = el.clientHeight || 0;
      setRange({
        top: el.scrollTop / full,
        height: Math.max(0.04, view / full),
      });
    }
    update();
    el.addEventListener("scroll", update, { passive: true });
    const ro = new ResizeObserver(update);
    ro.observe(el);
    return () => { el.removeEventListener("scroll", update); ro.disconnect(); };
  }, [scrollRef, corpus.length]);

  // Drag-to-scroll on the track. Clicking anywhere on the track centers the
  // viewport band at the cursor; dragging keeps it following the cursor.
  // Wheel events forward to the underlying scroll container so the rail feels
  // like part of the same scroll surface.
  function scrollToClientY(clientY) {
    const el = scrollRef?.current;
    const track = trackRef.current;
    if (!el || !track) return;
    const tr = track.getBoundingClientRect();
    const frac = Math.max(0, Math.min(1, (clientY - tr.top) / tr.height));
    const max = el.scrollHeight - el.clientHeight;
    // Place the cursor at the CENTER of the visible viewport band (not the top)
    // so the gesture feels anchored to where you're looking.
    const target = frac * el.scrollHeight - el.clientHeight / 2;
    el.scrollTop = Math.max(0, Math.min(max, target));
  }
  function onTrackPointerDown(e) {
    if (e.button !== 0) return;
    // Let explicit tick clicks still route through onJump (via their own handler).
    if (e.target.classList?.contains("corpus-tick")) return;
    e.preventDefault();
    const track = trackRef.current;
    const el = scrollRef?.current;
    const prevBehavior = el?.style.scrollBehavior;
    if (el) el.style.scrollBehavior = "auto"; // override smooth during drag
    track.setPointerCapture(e.pointerId);
    scrollToClientY(e.clientY);
    function onMove(ev) { scrollToClientY(ev.clientY); }
    function onUp(ev) {
      track.releasePointerCapture?.(e.pointerId);
      if (el) el.style.scrollBehavior = prevBehavior || "";
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
    }
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
  }
  function onWheel(e) {
    const el = scrollRef?.current;
    if (!el) return;
    el.scrollTop += e.deltaY;
  }

  const n = corpus.length || 1;
  return (
    <aside className="corpus-rail" ref={railRef} title={`corpus · ${corpus.length} messages`} onWheel={onWheel}>
      <div
        className="corpus-rail-track"
        ref={trackRef}
        onPointerDown={onTrackPointerDown}
      >
        {corpus.map((m, i) => (
          <span
            key={m.id}
            className="corpus-tick"
            style={{ top: `${(i / n) * 100}%` }}
            data-selected={selectedIds.has(m.id) || undefined}
            data-role={m.role}
            data-current={m.pos === currentPos || undefined}
            title={`#${m.pos} · ${m.role}`}
            onClick={(e) => { e.stopPropagation(); onJump?.(m.id); }}
          />
        ))}
        <div
          className="corpus-rail-viewport"
          style={{ top: `${range.top * 100}%`, height: `${range.height * 100}%` }}
          aria-hidden
        />
      </div>
    </aside>
  );
}

// --- prereq marker (footnote in the margin) -------------------------------
// A small superscript-style number. Hover shows the snippet. Click opens
// the drawer with full score breakdown. Cross-thread pulls render with ↗.
function PrereqMarker({ n, msg, sel, onOpen }) {
  const crossThread = sel?.crossThread;
  const score = sel?.score ?? 0;
  const strength = score > 0.85 ? "strong" : score > 0.6 ? "mid" : "weak";
  const label = crossThread ? `↗ from ${sel.threadName}: "${sel.snippet}"` : `#${msg?.pos}: ${msg?.text || ""}`;

  return (
    <button
      className="prq-mark"
      data-strength={strength}
      data-cross={crossThread || undefined}
      title={label}
      onClick={() => onOpen(msg?.id || sel.id, sel)}
    >
      <span className="prq-mark-n">{n}</span>
    </button>
  );
}

// --- tool call (tucked into response) -------------------------------------
function ToolCall({ t }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="toolcall" data-open={open}>
      <div className="toolcall-head" onClick={() => setOpen(!open)}>
        <span className="chev"><IconChevron size={10} /></span>
        <span className="toolcall-name">{t.name}</span>
        <span className="toolcall-arg">{t.args}</span>
        <span className="toolcall-status" data-s={t.status}>{t.status}</span>
      </div>
      {open && <div className="toolcall-body">{t.result}</div>}
    </div>
  );
}

function Subagent({ sub }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="subagent">
      <div className="subagent-head" onClick={() => setOpen(!open)}>
        <IconBranch size={13} />
        <span className="subagent-task">{sub.task}</span>
        <span className="subagent-meta">{sub.status} · {sub.rounds} rounds</span>
        <span className="chev" style={{ transform: open ? "rotate(90deg)" : "none", transition: "transform 0.15s" }}>
          <IconChevron size={11} />
        </span>
      </div>
      {open && <div className="subagent-body">{sub.result}</div>}
    </div>
  );
}

// --- plan mode --------------------------------------------------------------
// When a turn proposes a plan (structured steps) it renders with approve/
// reject controls inline. Approval kicks off execution (mocked).
function PlanBlock({ plan }) {
  const [decision, setDecision] = useState(null); // null | "approved" | "rejected"
  return (
    <div className="plan-block" data-decision={decision}>
      <header className="plan-head">
        <span className="plan-kind">plan · {plan.steps.length} steps</span>
        <span className="plan-rationale">{plan.rationale}</span>
      </header>
      <ol className="plan-steps">
        {plan.steps.map((s, i) => (
          <li key={i} className="plan-step">
            <span className="plan-step-n">{String(i + 1).padStart(2, "0")}</span>
            <span className="plan-step-text">{s.text}</span>
            {s.writes && <span className="plan-step-writes">writes <code>{s.writes}</code></span>}
          </li>
        ))}
      </ol>
      {decision === null ? (
        <div className="plan-actions">
          <button className="plan-approve" onClick={() => setDecision("approved")}>approve &amp; execute</button>
          <button className="plan-edit">edit plan</button>
          <button className="plan-reject" onClick={() => setDecision("rejected")}>reject</button>
          <span className="plan-hint">{IS_MAC ? "⌘↵" : "Ctrl+↵"} to approve · {IS_MAC ? "⌘⇧↵" : "Ctrl+Shift+↵"} autonomous</span>
        </div>
      ) : (
        <div className="plan-resolved">
          {decision === "approved"
            ? <>approved · executing step 1/{plan.steps.length}…</>
            : <>rejected · continue the thread to revise</>}
        </div>
      )}
    </div>
  );
}

// --- one turn ---------------------------------------------------------------
// Prompt (pulled out of the flow, italic serif) → marginalia → response.
// Position number is a discreet marker in the gutter, not a badge.
function Turn({ prompt, response, selection, corpusMap, onOpenCitation, last }) {
  const enrichment = RESPONSE_ENRICHMENT[response?.id] || {};
  const selected = (selection?.selected || []).filter(s => !s.phantom);
  const plan = enrichment.plan;

  return (
    <section className={`turn ${last ? "turn-last" : ""}`}>
      <div className="turn-gutter">
        <span className="turn-gutter-pos">#{prompt.pos}</span>
        {selected.length > 0 && (
          <div className="turn-gutter-marks">
            {selected.map((sel, i) => (
              <PrereqMarker
                key={sel.id}
                n={i + 1}
                msg={corpusMap[sel.id] || null}
                sel={sel}
                onOpen={onOpenCitation}
              />
            ))}
          </div>
        )}
      </div>

      <div className="turn-body">
        <div className="turn-prompt">{prompt.text}</div>

        <div className="turn-response">
          {enrichment.thinking && (
            <div className="thinking">{enrichment.thinking}</div>
          )}
          {response?.text && <p>{response.text}</p>}
          {plan && <PlanBlock plan={plan} />}
          {(enrichment.tools || []).map((t, i) => <ToolCall key={i} t={t} />)}
          {enrichment.subagent && <Subagent sub={enrichment.subagent} />}
        </div>
      </div>
    </section>
  );
}

// --- composer ---------------------------------------------------------------
function Composer({ onSend, scope, setScope, mode, setMode, streaming, hasPendingPlan, onOpenPalette }) {
  const [value, setValue] = useState("");
  const taRef = useRef(null);
  useEffect(() => {
    if (taRef.current) {
      taRef.current.style.height = "auto";
      taRef.current.style.height = Math.min(240, taRef.current.scrollHeight) + "px";
    }
  }, [value]);

  function submit() {
    if (!value.trim() || streaming) return;
    onSend(value.trim());
    setValue("");
  }

  function onKey(e) {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      submit();
    }
  }

  const placeholder = streaming
    ? "Agent is working…"
    : mode === "plan"
      ? "Describe what you want. The agent will propose a plan first."
      : mode === "autonomous"
        ? `Goal for the autonomous run. ${IS_MAC ? "⌘↵" : "Ctrl+↵"} to start.`
        : `Continue the thread. ${IS_MAC ? "⌘↵" : "Ctrl+↵"} to send.`;

  return (
    <div className="composer">
      <div className="composer-shell" data-mode={mode}>
        <textarea
          ref={taRef}
          className="composer-input"
          placeholder={placeholder}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={onKey}
          disabled={streaming}
          rows={2}
        />
        <div className="composer-foot">
          <button
            className="composer-jump"
            onClick={onOpenPalette}
            title={`Jump to anything (${kbdLabel("K")})`}
          >
            <IconCommand size={12} />
            <span>Jump to…</span>
          </button>
          <span className="composer-divider" aria-hidden="true" />
          <span className="mode-select">
            <button data-on={mode === "normal"}     onClick={() => setMode("normal")}>chat</button>
            <button data-on={mode === "plan"}       onClick={() => setMode("plan")}>plan</button>
            <button data-on={mode === "autonomous"} onClick={() => setMode("autonomous")}>autonomous</button>
          </span>
          <span className="scope-select" title="Selection scope">
            scope
            <button data-on={scope === "thread"} onClick={() => setScope("thread")}>thread</button>
            <button data-on={scope === "all"}    onClick={() => setScope("all")}>all</button>
          </span>
          <span className="spacer" />
          {value.length > 0 && (
            <span className="composer-hint">{value.length} chars · {IS_MAC ? "⌘↵" : "Ctrl+↵"}</span>
          )}
          <button
            className="composer-send"
            onClick={submit}
            disabled={!value.trim() || streaming}
            data-mode={mode}
          >
            {mode === "autonomous" ? "start run" : mode === "plan" ? "request plan" : "send"}
          </button>
        </div>
      </div>
    </div>
  );
}

// --- thread header ----------------------------------------------------------
// Editorial title + metadata line. Just the essentials — last-active time and
// branch provenance. No warmth, no corpus count (that's inspector territory).
function ThreadHeader({ thread, selectionCount }) {
  const parent = thread.parentId ? THREADS.find(t => t.id === thread.parentId) : null;
  return (
    <header className="thread-header">
      <h1>{thread.name}</h1>
      <div className="thread-header-meta">
        <span>last active <b>{thread.lastActive || "—"}</b></span>
        {parent && (
          <span>branched from <b>{parent.name}</b> at #{thread.branchAt}</span>
        )}
      </div>
    </header>
  );
}

// Empty-state for a fresh branch or new thread. This is a DOCUMENT surface,
// not a chat — so an empty branch gets an editorial note explaining what a
// branch inherits and what happens when you send the first prompt.
function EmptyThread({ thread }) {
  const parent = thread.parentId ? THREADS.find(t => t.id === thread.parentId) : null;
  const isFresh = (thread.msgCount ?? 0) === 0;
  if (parent) {
    return (
      <div className="empty-thread">
        <div className="empty-thread-rule">
          <span>branch point · <b>{parent.name}</b> #{thread.branchAt}</span>
        </div>
        <p className="empty-thread-lede">
          This thread branches from <b>{parent.name}</b> at position <b>#{thread.branchAt}</b>.
          Everything above that point is inherited; everything you send here diverges.
        </p>
        <dl className="empty-thread-facts">
          <div><dt>inherited corpus</dt><dd>{thread.branchAt} messages · cold-warm on arrival</dd></div>
          <div><dt>carry-forward QUDs</dt><dd>3 open questions from parent at branch point</dd></div>
          <div><dt>cross-thread edges</dt><dd>preserved, re-scored against new trajectory</dd></div>
        </dl>
        <p className="empty-thread-hint">
          Send a message below to write the first divergent turn.
        </p>
      </div>
    );
  }
  if (isFresh) {
    return (
      <div className="empty-thread">
        <p className="empty-thread-lede">
          A fresh thread. No corpus yet. Your first message becomes #1.
        </p>
        <p className="empty-thread-hint">
          Type below to begin. RRC will have nothing to select from on turn one — that's fine.
        </p>
      </div>
    );
  }
  // Placeholder: thread exists with messages but corpus not available in this
  // demo build. Show a short editorial note rather than a blank document.
  return (
    <div className="empty-thread">
      <div className="empty-thread-rule">
        <span>corpus preview · not loaded in this demo</span>
      </div>
      <p className="empty-thread-lede">
        <b>{thread.name}</b> has <b>{thread.msgCount}</b> messages.
        The corpus isn't loaded into this demo — open <b>Auth Module</b> for a fully-rendered thread.
      </p>
      <p className="empty-thread-hint">
        In the product, the full message history renders here exactly as Auth Module does.
      </p>
    </div>
  );
}

// --- thread view ------------------------------------------------------------
function ThreadView({ thread, onOpenCitation, streaming, onSend, scope, setScope, mode, setMode, onOpenPalette }) {
  const corpus = thread.corpus || [];
  const corpusMap = useMemo(() => Object.fromEntries(corpus.map(m => [m.id, m])), [corpus]);

  const turns = useMemo(() => {
    const out = [];
    for (let i = 0; i < corpus.length; i++) {
      const m = corpus[i];
      if (m.role !== "user") continue;
      const next = corpus[i + 1];
      if (next && next.role === "assistant") {
        out.push({
          prompt: m,
          response: next,
          selection: SELECTIONS[m.pos] || { selected: [], excluded: [] },
        });
      } else if (!next) {
        out.push({
          prompt: m,
          response: null,
          selection: SELECTIONS[m.pos] || { selected: [], excluded: [] },
          pending: true,
        });
      }
    }
    return out;
  }, [corpus]);

  const lastSelection = turns[turns.length - 1]?.selection;
  const selectionCount = (lastSelection?.selected || []).filter(s => !s.phantom).length;

  const streamRef = useRef(null);
  const lastTurnRef = useRef(null);

  useEffect(() => {
    if (lastTurnRef.current && streamRef.current) {
      const el = lastTurnRef.current;
      streamRef.current.scrollTop = el.offsetTop - streamRef.current.offsetTop - 12;
    }
  }, [thread.id]);

  useEffect(() => {
    if (streaming && streamRef.current) {
      streamRef.current.scrollTop = streamRef.current.scrollHeight;
    }
  }, [streaming]);

  const selectedIdsAll = new Set();
  turns.forEach(t => (t.selection?.selected || []).forEach(s => selectedIdsAll.add(s.id)));
  const currentPos = turns[turns.length - 1]?.prompt?.pos;

  function jumpToTurn(msgId) {
    const idx = turns.findIndex(t => t.prompt.id === msgId || t.response?.id === msgId);
    if (idx < 0) return;
    setTimeout(() => {
      const el = streamRef.current?.querySelectorAll("[data-turn]")[idx];
      if (el && streamRef.current) {
        streamRef.current.scrollTop = el.offsetTop - streamRef.current.offsetTop - 12;
      }
    }, 30);
  }

  return (
    <>
      <div className="thread-canvas">
        <div ref={streamRef} className="thread-stream scroll">
          <ThreadHeader thread={thread} selectionCount={selectionCount} />
          {turns.length === 0 && <EmptyThread thread={thread} />}
          {turns.map((t, i) => {
            const isLast = i === turns.length - 1;
            return (
              <div key={t.prompt.id} data-turn={i} ref={isLast ? lastTurnRef : null}>
                <Turn
                  prompt={t.prompt}
                  response={t.response || { id: "pending", text: streaming ? "" : "Waiting for agent." }}
                  selection={t.selection}
                  corpusMap={corpusMap}
                  onOpenCitation={onOpenCitation}
                  last={isLast}
                />
              </div>
            );
          })}
          {streaming && <StreamingTurn corpus={corpus} corpusMap={corpusMap} />}
        </div>
        <CorpusRail
          corpus={corpus}
          selectedIds={selectedIdsAll}
          currentPos={currentPos}
          onJump={jumpToTurn}
          scrollRef={streamRef}
        />
      </div>
      <Composer
        onSend={onSend}
        scope={scope} setScope={setScope}
        mode={mode} setMode={setMode}
        streaming={streaming}
        onOpenPalette={onOpenPalette}
      />
    </>
  );
}

// --- streaming turn ---------------------------------------------------------
// Appears at the bottom while the agent is working. Cycles through the RRC
// stages: selecting → restoring → thinking → responding.
function StreamingTurn({ corpus, corpusMap }) {
  const [stage, setStage] = useState("selecting");
  const [chars, setChars] = useState(0);

  useEffect(() => {
    const s1 = setTimeout(() => setStage("restoring"), 500);
    const s2 = setTimeout(() => setStage("thinking"), 1300);
    const s3 = setTimeout(() => setStage("responding"), 2100);
    return () => { clearTimeout(s1); clearTimeout(s2); clearTimeout(s3); };
  }, []);

  const FAKE_STREAM = "Surface a specific rate-limit error code (rate_limited) distinct from the generic auth error, so the client can show a user-visible retry-later message instead of forcing re-auth. Propagate Retry-After through the response.";

  useEffect(() => {
    if (stage !== "responding") return;
    const iv = setInterval(() => setChars(c => Math.min(c + 3, FAKE_STREAM.length)), 30);
    return () => clearInterval(iv);
  }, [stage]);

  const stageLabel = {
    selecting:  "rrc · scoring prerequisites",
    restoring:  "rrc · restoring messages",
    thinking:   "agent · thinking",
    responding: "agent · responding",
  }[stage];

  return (
    <section className="turn turn-streaming">
      <div className="turn-gutter">
        <span className="turn-gutter-pos live">LIVE</span>
        {stage !== "selecting" && (
          <div className="turn-gutter-marks">
            {["m24", "m22", "m6"].map((id, i) => (
              <PrereqMarker
                key={id}
                n={i + 1}
                msg={corpusMap[id]}
                sel={{ id, score: [0.94, 0.81, 0.72][i], source: "both" }}
                onOpen={() => {}}
              />
            ))}
          </div>
        )}
      </div>
      <div className="turn-body">
        <div className="streaming-stage">{stageLabel}<span className="dot-trail"><i/><i/><i/></span></div>
        {stage === "responding" && (
          <div className="turn-response">
            <p>{FAKE_STREAM.slice(0, chars)}<span className="caret" /></p>
          </div>
        )}
      </div>
    </section>
  );
}

Object.assign(window, { ThreadView });
