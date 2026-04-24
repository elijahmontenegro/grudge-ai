// Minimal inline SVG icons (monochrome, currentColor).
const Icon = ({ path, size = 14, stroke = 1.5 }) => (
  <svg width={size} height={size} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth={stroke} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    {path}
  </svg>
);

const IconSearch  = (p) => <Icon {...p} path={<><circle cx="7" cy="7" r="4.5" /><path d="M13.5 13.5l-3-3" /></>} />;
const IconPlus    = (p) => <Icon {...p} path={<><path d="M8 3v10M3 8h10" /></>} />;
const IconChevron = (p) => <Icon {...p} path={<path d="M6 4l4 4-4 4" />} />;
const IconX       = (p) => <Icon {...p} path={<><path d="M4 4l8 8M12 4l-8 8" /></>} />;
const IconArrow   = (p) => <Icon {...p} path={<><path d="M3 8h10M9 4l4 4-4 4" /></>} />;
const IconPlay    = (p) => <Icon {...p} path={<path d="M5 3l8 5-8 5z" fill="currentColor" stroke="none" />} />;
const IconPause   = (p) => <Icon {...p} path={<><rect x="4" y="3" width="3" height="10" fill="currentColor" stroke="none" /><rect x="9" y="3" width="3" height="10" fill="currentColor" stroke="none" /></>} />;
const IconStop    = (p) => <Icon {...p} path={<rect x="4" y="4" width="8" height="8" fill="currentColor" stroke="none" />} />;
const IconLayers  = (p) => <Icon {...p} path={<><path d="M8 2l6 3-6 3-6-3 6-3z" /><path d="M2 11l6 3 6-3" /></>} />;
const IconRipple  = (p) => <Icon {...p} path={<><circle cx="8" cy="8" r="2" /><circle cx="8" cy="8" r="5" /></>} />;
const IconBranch  = (p) => <Icon {...p} path={<><circle cx="4" cy="4" r="1.5" /><circle cx="4" cy="12" r="1.5" /><circle cx="12" cy="4" r="1.5" /><path d="M4 5.5v5M4 8h4a2 2 0 002-2V5.5" /></>} />;
const IconCommand = (p) => <Icon {...p} path={<path d="M6 10v-4M10 6v4M6 6h-2a1.5 1.5 0 110-3 1.5 1.5 0 011.5 1.5v1.5zM10 6h2a1.5 1.5 0 100-3 1.5 1.5 0 00-1.5 1.5v1.5zM6 10h-2a1.5 1.5 0 100 3 1.5 1.5 0 001.5-1.5V10zM10 10h2a1.5 1.5 0 110 3 1.5 1.5 0 01-1.5-1.5V10z" />} />;
const IconSettings = (p) => <Icon {...p} path={<><circle cx="8" cy="8" r="2" /><path d="M8 1v2M8 13v2M1 8h2M13 8h2M3 3l1.5 1.5M11.5 11.5L13 13M3 13l1.5-1.5M11.5 4.5L13 3" /></>} />;
const IconSkill   = (p) => <Icon {...p} path={<><path d="M8 1l2 4.5 5 .5-3.75 3.4L12.5 15 8 12.5 3.5 15l1.25-5.6L1 6l5-.5z" /></>} />;
const IconSun     = (p) => <Icon {...p} path={<><circle cx="8" cy="8" r="2.5" /><path d="M8 1.5v1.5M8 13v1.5M1.5 8h1.5M13 8h1.5M3 3l1.1 1.1M11.9 11.9L13 13M3 13l1.1-1.1M11.9 4.1L13 3" /></>} />;
const IconMoon    = (p) => <Icon {...p} path={<path d="M13.5 9.5A6 6 0 017 3a6 6 0 106.5 6.5z" />} />;
const IconChevronDouble = (p) => <Icon {...p} path={<path d="M4 4l4 4-4 4M9 4l4 4-4 4" />} />;
const IconPin     = (p) => <Icon {...p} path={<path d="M10 2l4 4-3 1-2 2-1 4-4-4 4-1 2-2z" />} />;
const IconArchive = (p) => <Icon {...p} path={<><path d="M2 4h12v3H2zM3 7v7h10V7" /><path d="M6 10h4" /></>} />;
const IconPanel   = (p) => <Icon {...p} path={<><rect x="2" y="3" width="12" height="10" rx="1" /><path d="M6 3v10" /></>} />;

// Spidey mark — a geometric web. Six nodes around a center, one accent node,
// lightweight threads. Sized by the `size` prop; uses currentColor for the
// threads and `accent` for the highlighted node.
function Spidey({ size = 18, accent = "var(--accent)" }) {
  const r = 7;                              // ring radius in viewBox units (0–20)
  const cx = 10, cy = 10;
  const pts = Array.from({ length: 6 }, (_, i) => {
    const a = (Math.PI * 2 * i) / 6 - Math.PI / 2;
    return [cx + r * Math.cos(a), cy + r * Math.sin(a)];
  });
  return (
    <svg width={size} height={size} viewBox="0 0 20 20" aria-hidden="true" style={{ display: "block" }}>
      {/* spokes */}
      {pts.map(([x, y], i) => (
        <line key={"s" + i} x1={cx} y1={cy} x2={x} y2={y} stroke="currentColor" strokeWidth="0.7" opacity="0.45" />
      ))}
      {/* outer hexagon */}
      {pts.map(([x, y], i) => {
        const [x2, y2] = pts[(i + 1) % 6];
        return <line key={"e" + i} x1={x} y1={y} x2={x2} y2={y2} stroke="currentColor" strokeWidth="0.7" opacity="0.45" />;
      })}
      {/* nodes */}
      {pts.map(([x, y], i) => (
        <circle key={"n" + i} cx={x} cy={y} r="1.1" fill={i === 0 ? accent : "currentColor"} />
      ))}
      <circle cx={cx} cy={cy} r="1.4" fill="currentColor" />
    </svg>
  );
}

Object.assign(window, { Icon, IconSearch, IconPlus, IconChevron, IconX, IconArrow, IconPlay, IconPause, IconStop, IconLayers, IconRipple, IconBranch, IconCommand, IconSettings, IconSkill, IconSun, IconMoon, IconChevronDouble, IconPin, IconArchive, IconPanel, Spidey });
