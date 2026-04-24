// Small shared UI primitives.
const { useState, useEffect, useRef, useMemo, useCallback } = React;

function WarmthBar({ value, max = 6 }) {
  // Logarithmic mapping. 0 => 0 bars, 203 => 6 bars.
  const lit = value === 0 ? 0 : Math.min(max, Math.max(1, Math.round(Math.log2(value + 1))));
  const bars = [];
  for (let i = 0; i < max; i++) {
    bars.push(<i key={i} data-on={i < lit} style={{ height: `${4 + i * 1.2}px` }} />);
  }
  return <span className="warmth-bar" aria-label={`warmth ${value}`}>{bars}</span>;
}

function StateDot({ state }) {
  return <span className="thread-dot" data-state={state} />;
}

function fmtScore(n) {
  return n.toFixed(2).replace(/^0/, "");
}

function ScoreBar({ value, kind }) {
  return <span className="bar" data-k={kind}><i style={{ width: `${Math.min(100, value * 100)}%` }} /></span>;
}

// Platform-neutral shortcut rendering. We're a multi-platform app, so ⌘
// would mislead Windows/Linux users. IS_MAC gates the glyph; everywhere else
// shows "Ctrl". Use <Kbd combo="k" /> in UI, kbdLabel("k") for tooltips.
const IS_MAC = typeof navigator !== "undefined" &&
  /mac|iphone|ipad|ipod/i.test(navigator.platform || navigator.userAgent || "");

function kbdLabel(key) {
  return (IS_MAC ? "⌘" : "Ctrl+") + key.toUpperCase();
}

function Kbd({ combo }) {
  // combo is just the letter key (e.g. "k", "n"). Modifier comes from platform.
  return (
    <kbd className="kbd">
      <span className="kbd-mod">{IS_MAC ? "⌘" : "Ctrl"}</span>
      <span className="kbd-key">{combo.toUpperCase()}</span>
    </kbd>
  );
}

Object.assign(window, { WarmthBar, StateDot, fmtScore, ScoreBar, IS_MAC, kbdLabel, Kbd });
