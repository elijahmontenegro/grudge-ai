function Tweaks({ state, setState }) {
  const set = (k, v) => setState(s => ({ ...s, [k]: v }));
  return (
    <div className="tweaks">
      <div className="tweaks-head">
        <IconSettings size={12} />
        tweaks
      </div>
      <div className="tweaks-body">
        <div className="tweak-row">
          <label>aesthetic</label>
          <div className="seg">
            <button data-on={state.aesthetic === "editorial"} onClick={() => set("aesthetic", "editorial")}>editorial</button>
            <button data-on={state.aesthetic === "terminal"} onClick={() => set("aesthetic", "terminal")}>terminal</button>
          </div>
        </div>
        <div className="tweak-row">
          <label>theme</label>
          <div className="seg">
            <button data-on={state.theme === "light"} onClick={() => set("theme", "light")}>light</button>
            <button data-on={state.theme === "dark"} onClick={() => set("theme", "dark")}>dark</button>
          </div>
        </div>
        <div className="tweak-row">
          <label>default view mode</label>
          <div className="seg">
            <button data-on={state.viewMode === "selections"} onClick={() => set("viewMode", "selections")}>selections</button>
            <button data-on={state.viewMode === "corpus"} onClick={() => set("viewMode", "corpus")}>corpus</button>
            <button data-on={state.viewMode === "minimal"} onClick={() => set("viewMode", "minimal")}>minimal</button>
          </div>
        </div>
        <div className="tweak-row">
          <label>density</label>
          <div className="seg">
            <button data-on={state.density === "comfortable"} onClick={() => set("density", "comfortable")}>comfortable</button>
            <button data-on={state.density === "compact"} onClick={() => set("density", "compact")}>compact</button>
          </div>
        </div>
        <div className="tweak-row">
          <label>screen</label>
          <div className="seg">
            <button data-on={state.screen === "thread"} onClick={() => set("screen", "thread")}>thread</button>
            <button data-on={state.screen === "home"} onClick={() => set("screen", "home")}>home</button>
            <button data-on={state.screen === "firstrun"} onClick={() => set("screen", "firstrun")}>first-run</button>
          </div>
        </div>
      </div>
    </div>
  );
}

Object.assign(window, { Tweaks });
