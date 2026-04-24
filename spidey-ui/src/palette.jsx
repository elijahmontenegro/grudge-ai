function CommandPalette({ open, onClose, onRun, threads, onOpenThread }) {
  const [q, setQ] = useState("");
  const [idx, setIdx] = useState(0);
  const inputRef = useRef(null);

  useEffect(() => {
    if (open) {
      setQ("");
      setIdx(0);
      setTimeout(() => inputRef.current?.focus(), 30);
    }
  }, [open]);

  const filtered = PALETTE_ITEMS.filter(i =>
    i.label.toLowerCase().includes(q.toLowerCase()) ||
    i.kind.toLowerCase().includes(q.toLowerCase())
  );

  useEffect(() => {
    function onKey(e) {
      if (!open) return;
      if (e.key === "Escape") { onClose(); }
      else if (e.key === "ArrowDown") { e.preventDefault(); setIdx(i => Math.min(filtered.length - 1, i + 1)); }
      else if (e.key === "ArrowUp") { e.preventDefault(); setIdx(i => Math.max(0, i - 1)); }
      else if (e.key === "Enter") {
        e.preventDefault();
        const item = filtered[idx];
        if (item) {
          if (item.kind === "thread") {
            const t = threads.find(x => x.name === item.label);
            if (t) { onOpenThread(t.id); onClose(); return; }
          }
          onRun(item);
          onClose();
        }
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, filtered, idx, threads]);

  if (!open) return null;

  const grouped = {};
  filtered.forEach(i => (grouped[i.kind] ||= []).push(i));

  let runningIdx = -1;
  return (
    <div className="palette-overlay" onClick={onClose}>
      <div className="palette" onClick={(e) => e.stopPropagation()}>
        <input
          ref={inputRef}
          className="palette-input"
          placeholder="Search, run a command, switch thread, invoke a skill…"
          value={q}
          onChange={(e) => { setQ(e.target.value); setIdx(0); }}
        />
        <div className="palette-results scroll">
          {Object.entries(grouped).map(([kind, items]) => (
            <div key={kind}>
              <div className="palette-group-head">{kind === "thread" ? "threads" : kind}</div>
              {items.map(item => {
                runningIdx++;
                const sel = runningIdx === idx;
                return (
                  <div
                    key={item.label}
                    className="palette-item"
                    data-sel={sel}
                    onMouseEnter={() => setIdx(runningIdx)}
                    onClick={() => {
                      if (item.kind === "thread") {
                        const t = threads.find(x => x.name === item.label);
                        if (t) { onOpenThread(t.id); onClose(); return; }
                      }
                      onRun(item); onClose();
                    }}
                  >
                    <span className="icon">
                      {item.kind === "action" && <IconArrow />}
                      {item.kind === "thread" && <StateDot state="idle" />}
                      {item.kind === "skill" && <IconSkill />}
                      {item.kind === "search" && <IconSearch />}
                      {item.kind === "setting" && <IconSettings />}
                    </span>
                    <span>{item.label}</span>
                    <span className="kind">{item.hint}</span>
                  </div>
                );
              })}
            </div>
          ))}
          {filtered.length === 0 && (
            <div style={{ padding: 20, textAlign: "center", color: "var(--muted)", fontFamily: "var(--mono)", fontSize: 12 }}>
              No results. Press ⏎ to start a new thread with this prompt.
            </div>
          )}
        </div>
        <div className="palette-foot">
          <span><kbd>↑↓</kbd> navigate</span>
          <span><kbd>⏎</kbd> run</span>
          <span><kbd>esc</kbd> close</span>
        </div>
      </div>
    </div>
  );
}

Object.assign(window, { CommandPalette });
