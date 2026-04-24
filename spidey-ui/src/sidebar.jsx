// Sidebar rebuild for scale.
//
// Design decisions:
// - Collapsible to a 52px icon rail with mark + thread dots.
// - Search is top-level (threads are the primary object).
// - Sections: Pinned · Today · This week · Older · Archived. Each collapsible.
// - Branches are HIDDEN by default under their parent and expand on a tiny caret.
//   (This keeps the tree quiet when you aren't forking.)
// - No warmth, no bar-chart glyphs, no running-pulse per-row.
//   Autonomous state gets a single subtle side-tick, that's it.
// - User menu (profile + settings + theme) lives at the bottom.

function groupThreads(threads, query) {
  const q = query.trim().toLowerCase();
  const matches = threads.filter(t =>
    !q || t.name.toLowerCase().includes(q) || t.id.toLowerCase().includes(q)
  );

  const roots = matches.filter(t => !t.parentId);
  const childrenByParent = {};
  matches.filter(t => t.parentId).forEach(c => {
    (childrenByParent[c.parentId] ||= []).push(c);
  });

  // If the user searched, collapse groups into a flat "results" section.
  if (q) {
    return [{ key: "results", label: "results", threads: roots, childrenByParent }];
  }

  const pinned = roots.filter(t => t.pinned && !t.archived);
  const archived = roots.filter(t => t.archived);
  const active = roots.filter(t => !t.pinned && !t.archived);

  function bucket(t) {
    const la = (t.lastActive || "").toLowerCase();
    if (la === "now" || /\dm$/.test(la) || /\dh$/.test(la)) return "today";
    if (la === "yesterday" || /^[1-6]d$/.test(la)) return "week";
    return "older";
  }
  const today  = active.filter(t => bucket(t) === "today");
  const week   = active.filter(t => bucket(t) === "week");
  const older  = active.filter(t => bucket(t) === "older");

  const sections = [];
  if (pinned.length)   sections.push({ key: "pinned",   label: "pinned",      threads: pinned, childrenByParent });
  if (today.length)    sections.push({ key: "today",    label: "today",       threads: today, childrenByParent });
  if (week.length)     sections.push({ key: "week",     label: "this week",   threads: week, childrenByParent });
  if (older.length)    sections.push({ key: "older",    label: "older",       threads: older, childrenByParent });
  if (archived.length) sections.push({ key: "archived", label: "archived",    threads: archived, childrenByParent, defaultCollapsed: true });
  return sections;
}

function ThreadRow({ thread, active, onClick, child, onToggleBranches, showBranches, hasBranches }) {
  const autonomous = (thread.elapsed || "").startsWith("autonomous");
  return (
    <div
      className="thread-row"
      data-active={active}
      data-child={child ? "true" : undefined}
      data-autonomous={autonomous ? "true" : undefined}
      onClick={onClick}
    >
      {hasBranches ? (
        <button
          className="thread-branch-toggle"
          onClick={(e) => { e.stopPropagation(); onToggleBranches(); }}
          data-open={showBranches}
          title={showBranches ? "Hide branches" : "Show branches"}
        >
          <IconChevron size={9} />
        </button>
      ) : (
        <span className="thread-branch-slot" />
      )}
      <span className="thread-row-name">{thread.name}</span>
      {autonomous && <span className="thread-row-tick" title="autonomous run" />}
      {thread.pinned && !child && <span className="thread-row-pin" title="pinned"><IconPin size={9} /></span>}
    </div>
  );
}

function Section({ label, defaultCollapsed, children, count }) {
  const [open, setOpen] = useState(!defaultCollapsed);
  return (
    <div className="sb-section" data-open={open}>
      <button className="sb-section-head" onClick={() => setOpen(v => !v)}>
        <IconChevron size={9} />
        <span>{label}</span>
        <span className="sb-section-count">{count}</span>
      </button>
      {open && <div className="sb-section-body">{children}</div>}
    </div>
  );
}

// UserMenu is rendered as a fixed-position popover anchored to the element
// passed via `anchorRef`. We use fixed positioning so it escapes both the
// sidebar's overflow clip AND the narrow collapsed-rail width.
function UserMenu({ open, anchorRef, placement = "top", onClose, theme, onToggleTheme, onOpenFirstRun }) {
  const ref = useRef(null);
  const [pos, setPos] = useState(null);
  useEffect(() => {
    if (!open) return;
    function place() {
      const a = anchorRef?.current;
      if (!a) return;
      const r = a.getBoundingClientRect();
      if (placement === "right") {
        // Collapsed rail: pop to the right of the avatar button.
        setPos({ left: r.right + 8, bottom: Math.max(8, window.innerHeight - r.bottom) });
      } else {
        // Expanded sidebar: pop above the user pill. Use a min width so items
        // like "Model providers" and "Sign out" don't wrap in narrow sidebars.
        const width = Math.max(r.width, 240);
        setPos({ left: r.left, width, bottom: window.innerHeight - r.top + 6 });
      }
    }
    place();
    function onDoc(e) { if (ref.current && !ref.current.contains(e.target) && !anchorRef?.current?.contains(e.target)) onClose(); }
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    document.addEventListener("mousedown", onDoc);
    return () => {
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
      document.removeEventListener("mousedown", onDoc);
    };
  }, [open, anchorRef, placement, onClose]);
  if (!open || !pos) return null;
  const style = { position: "fixed", ...pos };
  return (
    <div className="user-menu" ref={ref} style={style}>
      <div className="user-menu-head">
        <div className="user-menu-name">{USER.name}</div>
        <div className="user-menu-sub">{USER.host}</div>
      </div>
      <div className="user-menu-group">
        <button className="user-menu-item" onClick={onToggleTheme}>
          <span className="user-menu-icon">{theme === "dark" ? <IconSun size={13} /> : <IconMoon size={13} />}</span>
          <span>{theme === "dark" ? "Light appearance" : "Dark appearance"}</span>
        </button>
        <button className="user-menu-item">
          <span className="user-menu-icon"><IconSettings size={13} /></span>
          <span>Preferences</span>
          <span className="user-menu-hint">{kbdLabel(",")}</span>
        </button>
        <button className="user-menu-item" onClick={onOpenFirstRun}>
          <span className="user-menu-icon"><IconLayers size={13} /></span>
          <span>Model providers</span>
        </button>
      </div>
      <div className="user-menu-group">
        <button className="user-menu-item">
          <span className="user-menu-icon"><IconCommand size={13} /></span>
          <span>Keyboard shortcuts</span>
          <span className="user-menu-hint">?</span>
        </button>
        <button className="user-menu-item"><span className="user-menu-icon" /><span>Sign out</span></button>
      </div>
    </div>
  );
}

function Sidebar({ threads, activeId, onSelect, onNew, onOpenHome, onOpenPalette, view, collapsed, onToggleCollapsed, theme, onToggleTheme, onOpenFirstRun }) {
  const [query, setQuery] = useState("");
  const [expandedBranches, setExpandedBranches] = useState({});
  const [menuOpen, setMenuOpen] = useState(false);
  const userAnchorRef = useRef(null);

  const sections = useMemo(() => groupThreads(threads, query), [threads, query]);

  if (collapsed) {
    // Rebuild the same bucket structure (pinned · today · week · older), then
    // flatten into a list with divider markers between non-empty buckets so
    // the partitions visible in the expanded view still read in the rail.
    const flatBuckets = sections
      .filter(s => s.key !== "archived")
      .map(s => ({ key: s.key, threads: s.threads }));
    return (
      <aside className="sidebar sidebar-rail">
        <button className="rail-brand" onClick={onToggleCollapsed} title="Expand sidebar">
          <Spidey size={20} />
        </button>
        <button className="rail-btn" onClick={onNew} title="New thread"><IconPlus size={13} /></button>
        <div className="rail-divider" />
        <div className="rail-list">
          {flatBuckets.map((bucket, bi) => (
            <React.Fragment key={bucket.key}>
              {bi > 0 && bucket.threads.length > 0 && <div className="rail-bucket-div" />}
              {bucket.threads.slice(0, 6).map(t => (
                <button
                  key={t.id}
                  className="rail-thread"
                  data-active={view === "thread" && activeId === t.id}
                  data-autonomous={(t.elapsed || "").startsWith("autonomous") ? "true" : undefined}
                  onClick={() => onSelect(t.id)}
                  title={`${t.name}  ·  ${bucket.key}`}
                >
                  <span>{t.name.slice(0, 2)}</span>
                </button>
              ))}
            </React.Fragment>
          ))}
        </div>
        <div style={{ flex: 1 }} />
        <button ref={userAnchorRef} className="rail-btn" onClick={() => setMenuOpen(o => !o)} title={USER.name}>
          <span className="rail-avatar">{USER.initials}</span>
        </button>
        <UserMenu open={menuOpen} anchorRef={userAnchorRef} placement="right" onClose={() => setMenuOpen(false)} theme={theme} onToggleTheme={onToggleTheme} onOpenFirstRun={onOpenFirstRun} />
      </aside>
    );
  }

  return (
    <aside className="sidebar">
      <div className="sb-head">
        <div className="sb-brand">
          <Spidey size={18} />
          <span className="sb-brand-text">spidey</span>
        </div>
        <button className="sb-icon-btn" onClick={onToggleCollapsed} title="Collapse sidebar">
          <IconPanel size={13} />
        </button>
      </div>

      <div className="sb-search-row">
        <div className="sb-search">
          <IconSearch size={12} />
          <input
            type="text"
            placeholder="Filter threads"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          {query && (
            <button className="sb-search-clear" onClick={() => setQuery("")} title="Clear">
              <IconX size={10} />
            </button>
          )}
        </div>
        <button className="sb-new-btn" onClick={onNew} title={`New thread (${kbdLabel("N")})`}>
          <IconPlus size={12} />
          <span>New</span>
        </button>
      </div>

      <div className="sb-list scroll">
        <div
          className="thread-row sb-home"
          data-active={view === "home"}
          onClick={onOpenHome}
        >
          <span className="thread-branch-slot" />
          <span className="thread-row-name">Home</span>
        </div>

        {sections.map(section => (
          <Section
            key={section.key}
            label={section.label}
            count={section.threads.length}
            defaultCollapsed={section.defaultCollapsed}
          >
            {section.threads.map(t => {
              const kids = section.childrenByParent[t.id] || [];
              const expanded = !!expandedBranches[t.id];
              return (
                <React.Fragment key={t.id}>
                  <ThreadRow
                    thread={t}
                    active={view === "thread" && activeId === t.id}
                    onClick={() => onSelect(t.id)}
                    hasBranches={kids.length > 0}
                    showBranches={expanded}
                    onToggleBranches={() => setExpandedBranches(e => ({ ...e, [t.id]: !e[t.id] }))}
                  />
                  {expanded && kids.map(c => (
                    <ThreadRow
                      key={c.id}
                      thread={c}
                      child
                      active={view === "thread" && activeId === c.id}
                      onClick={() => onSelect(c.id)}
                    />
                  ))}
                </React.Fragment>
              );
            })}
            {section.threads.length === 0 && (
              <div className="sb-empty">no matches</div>
            )}
          </Section>
        ))}
      </div>

      <div className="sb-footer">
        <div ref={userAnchorRef} className="sb-user" onClick={() => setMenuOpen(o => !o)}>
          <span className="sb-avatar">{USER.initials}</span>
          <div className="sb-user-text">
            <div className="sb-user-name">{USER.name}</div>
            <div className="sb-user-handle">@{USER.handle}</div>
          </div>
          <IconChevron size={10} />
        </div>
        <UserMenu open={menuOpen} anchorRef={userAnchorRef} placement="top" onClose={() => setMenuOpen(false)} theme={theme} onToggleTheme={onToggleTheme} onOpenFirstRun={onOpenFirstRun} />
      </div>
    </aside>
  );
}

Object.assign(window, { Sidebar });
