const TWEAK_DEFAULTS = /*EDITMODE-BEGIN*/{
  "aesthetic": "editorial",
  "theme": "light",
  "viewMode": "selections",
  "density": "comfortable",
  "screen": "thread"
}/*EDITMODE-END*/;

function App() {
  // Load persisted state
  const persisted = (() => {
    try { return JSON.parse(localStorage.getItem("spidey.ui") || "{}"); }
    catch { return {}; }
  })();

  const [view, setView] = useState(persisted.view || "thread");
  const [activeId, setActiveId] = useState(persisted.activeId || "t-auth");
  const [sidebarCollapsed, setSidebarCollapsed] = useState(persisted.sidebarCollapsed || false);
  const [drawerCitation, setDrawerCitation] = useState(null);
  const [drawerSelection, setDrawerSelection] = useState(null);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [streaming, setStreaming] = useState(false);
  const [scope, setScope] = useState("thread");
  const [mode, setMode] = useState("normal");
  const [tweaks, setTweaks] = useState({ ...TWEAK_DEFAULTS, ...persisted.tweaks });
  const [tweaksVisible, setTweaksVisible] = useState(false);

  // Persist
  useEffect(() => {
    localStorage.setItem("spidey.ui", JSON.stringify({ view, activeId, tweaks, sidebarCollapsed }));
  }, [view, activeId, tweaks, sidebarCollapsed]);

  // Apply tweaks to body
  useEffect(() => {
    document.documentElement.dataset.theme = tweaks.theme;
    document.body.dataset.aesthetic = tweaks.aesthetic;
    document.body.dataset.density = tweaks.density;
  }, [tweaks]);

  // Edit-mode host bridge
  useEffect(() => {
    const listener = (e) => {
      if (e.data?.type === "__activate_edit_mode") setTweaksVisible(true);
      if (e.data?.type === "__deactivate_edit_mode") setTweaksVisible(false);
    };
    window.addEventListener("message", listener);
    window.parent.postMessage({ type: "__edit_mode_available" }, "*");
    return () => window.removeEventListener("message", listener);
  }, []);

  // Global shortcuts
  useEffect(() => {
    function onKey(e) {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPaletteOpen(true);
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // Screen override via tweaks
  useEffect(() => {
    if (tweaks.screen === "firstrun") setView("firstrun");
    else if (tweaks.screen === "home") setView("home");
    else if (tweaks.screen === "thread") setView("thread");
  }, [tweaks.screen]);

  const thread = THREADS.find(t => t.id === activeId);
  const corpus = thread?.corpus || [];
  const corpusMap = useMemo(() => Object.fromEntries(corpus.map(m => [m.id, m])), [corpus]);

  function openCitation(id, sel) {
    setDrawerCitation(sel || { id });
    // find the selection this citation is part of
    for (const key of Object.keys(SELECTIONS)) {
      const s = SELECTIONS[key];
      if (s.selected?.some(x => x.id === id)) {
        setDrawerSelection(s);
        return;
      }
    }
    setDrawerSelection({ selected: [], excluded: [] });
  }

  function closeDrawer() {
    setDrawerCitation(null);
    setDrawerSelection(null);
  }

  function sendMessage(text) {
    setStreaming(true);
    setTimeout(() => setStreaming(false), 4500);
  }

  function quickStart(text) {
    setActiveId("t-new");
    setView("thread");
    setStreaming(true);
    setTimeout(() => setStreaming(false), 4500);
  }

  const drawerOpen = !!drawerCitation;
  const chromeless = view === "firstrun";

  return (
    <div className="app" data-drawer-open={drawerOpen} data-chromeless={chromeless} data-sidebar-collapsed={sidebarCollapsed ? "true" : undefined}>
      {!chromeless && (
        <Sidebar
          threads={THREADS}
          activeId={activeId}
          view={view}
          onSelect={(id) => { setActiveId(id); setView("thread"); closeDrawer(); }}
          onOpenHome={() => { setView("home"); closeDrawer(); }}
          onNew={() => { setActiveId("t-new"); setView("thread"); closeDrawer(); }}
          onOpenPalette={() => setPaletteOpen(true)}
          collapsed={sidebarCollapsed}
          onToggleCollapsed={() => setSidebarCollapsed(v => !v)}
          theme={tweaks.theme}
          onToggleTheme={() => setTweaks(t => ({ ...t, theme: t.theme === "dark" ? "light" : "dark" }))}
          onOpenFirstRun={() => { setView("firstrun"); setTweaks(s => ({ ...s, screen: "firstrun" })); }}
        />
      )}

      <main className="main">
        {!chromeless && (
          <Topbar
            view={view}
            thread={thread}
            onOpenHome={() => setView("home")}
          />
        )}

        {view === "home" && (
          <Home
            threads={THREADS}
            onOpenThread={(id) => { setActiveId(id); setView("thread"); }}
            onQuickStart={quickStart}
            onOpenPalette={() => setPaletteOpen(true)}
          />
        )}
        {view === "thread" && thread && (
          <ThreadView
            thread={thread}
            onOpenCitation={openCitation}
            streaming={streaming}
            onSend={sendMessage}
            scope={scope} setScope={setScope}
            mode={mode} setMode={setMode}
            defaultViewMode={tweaks.viewMode}
            onOpenPalette={() => setPaletteOpen(true)}
          />
        )}
        {view === "firstrun" && (
          <FirstRun onComplete={() => { setView("home"); setTweaks(s => ({ ...s, screen: "home" })); }} />
        )}
      </main>

      {drawerOpen && (
        <Drawer
          open={drawerOpen}
          citation={drawerCitation}
          selection={drawerSelection}
          corpus={corpus}
          corpusMap={corpusMap}
          onClose={closeDrawer}
        />
      )}

      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        onRun={(item) => {
          if (item.label === "New thread") { setActiveId("t-new"); setView("thread"); }
        }}
        threads={THREADS}
        onOpenThread={(id) => { setActiveId(id); setView("thread"); closeDrawer(); }}
      />

      {tweaksVisible && <Tweaks state={tweaks} setState={setTweaks} />}
    </div>
  );
}

function Topbar({ view, thread, onOpenHome }) {
  return (
    <div className="topbar">
      <div className="crumbs">
        <span style={{ cursor: "pointer" }} onClick={onOpenHome}>spidey</span>
        {view === "thread" && thread && <><span>/</span><b>{thread.name}</b></>}
        {view === "home" && <><span>/</span><b>home</b></>}
      </div>
      <span className="spacer" />
      {view === "thread" && thread?.elapsed?.startsWith("autonomous") && (
        <span className="tb-autonomous-chip" title="autonomous run">
          <span className="tb-tick" />
          {thread.elapsed}
        </span>
      )}
    </div>
  );
}

const root = ReactDOM.createRoot(document.getElementById("root"));
root.render(<App />);
