import { useEffect, useState } from 'react'
import { useMutation, useQuery } from '@apollo/client/react'
import { GET_SETTINGS, UPDATE_SETTINGS } from '@/graphql/operations'

interface MCPServer {
  name: string
  endpoint: string
  enabled: boolean
}

interface HookConfig {
  event: string
  command: string
  match: string
  timeout: string
}

const HOOK_EVENTS = [
  'before_tool',
  'after_tool',
  'before_send',
  'after_receive',
  'on_start',
  'on_stop',
]

type Perm = 'allow' | 'ask' | 'deny'
type PermissionsMap = Record<string, Perm>

// The tool set the backend enforces permissions on (service/agent/tools.go).
// Ordered by risk so destructive / ask-gated tools sit at the top.
const KNOWN_TOOLS: Array<{ name: string; desc: string; defaultPerm: Perm }> = [
  { name: 'Bash', desc: 'execute shell commands', defaultPerm: 'ask' },
  { name: 'FileWrite', desc: 'create / overwrite files', defaultPerm: 'ask' },
  { name: 'FileEdit', desc: 'edit files in place', defaultPerm: 'ask' },
  { name: 'NotebookEdit', desc: 'edit Jupyter notebook cells', defaultPerm: 'ask' },
  { name: 'Agent', desc: 'spawn a subagent', defaultPerm: 'ask' },
  { name: 'SendMessage', desc: 'message a running subagent', defaultPerm: 'ask' },
  { name: 'Skill', desc: 'invoke a skill', defaultPerm: 'ask' },
  { name: 'TodoWrite', desc: 'create task list', defaultPerm: 'ask' },
  { name: 'TaskCreate', desc: 'create a task', defaultPerm: 'ask' },
  { name: 'TaskUpdate', desc: 'update a task', defaultPerm: 'ask' },
  { name: 'TaskStop', desc: 'stop a running task', defaultPerm: 'ask' },
  { name: 'EnterPlanMode', desc: 'enter plan mode', defaultPerm: 'ask' },
  { name: 'ExitPlanMode', desc: 'exit plan mode', defaultPerm: 'ask' },
  { name: 'FileRead', desc: 'read files', defaultPerm: 'allow' },
  { name: 'Glob', desc: 'find files by pattern', defaultPerm: 'allow' },
  { name: 'Grep', desc: 'search file contents', defaultPerm: 'allow' },
  { name: 'WebSearch', desc: 'search the web', defaultPerm: 'allow' },
  { name: 'WebFetch', desc: 'fetch a URL', defaultPerm: 'allow' },
  { name: 'AskUserQuestion', desc: 'ask the user for input', defaultPerm: 'allow' },
  { name: 'TaskGet', desc: 'read a task', defaultPerm: 'allow' },
  { name: 'TaskList', desc: 'list tasks', defaultPerm: 'allow' },
  { name: 'TaskOutput', desc: 'read a task output', defaultPerm: 'allow' },
]

interface ProviderConfig {
  adapter: string
  model: string
  base_url?: string
  api_key?: string
}

// Keys match the service/config/settings.go backend schema exactly.
// Writing with different keys would wipe the existing config on save
// because UpdateSettings.Providers is a full replace, not merge.
type ProvidersMap = Record<string, ProviderConfig>

interface Preferences {
  name?: string
  [k: string]: unknown
}

// Matches service/config/settings.go:EngineConfig. Tunable at runtime; backend
// recomputes edge scores under the new config at walk time, so edits take
// effect on the very next turn with no rebuild.
interface EngineConfig {
  edge_threshold: number
  score_floor: number
  weight_ce: number
  weight_temp: number
  radius_size: number
  rerank_top_k: number
}

const ENGINE_DEFAULT: EngineConfig = {
  edge_threshold: 0.35,
  score_floor: 0.01,
  weight_ce: 0.6,
  weight_temp: 0.4,
  radius_size: 10,
  rerank_top_k: 64,
}

const ENGINE_FIELDS: Array<{
  key: keyof EngineConfig
  label: string
  hint: string
  step: number
  integer?: boolean
}> = [
  {
    key: 'edge_threshold',
    label: 'edge threshold',
    hint: 'fused-score minimum for an edge to enter the DAG (0 = accept all)',
    step: 0.01,
  },
  {
    key: 'score_floor',
    label: 'score floor',
    hint: 'effective-score minimum for a selected message (0 = no cutoff)',
    step: 0.01,
  },
  {
    key: 'weight_ce',
    label: 'weight · reranker',
    hint: 'semantic axis weight. 0 = pure-temporal scoring',
    step: 0.05,
  },
  {
    key: 'weight_temp',
    label: 'weight · temporal',
    hint: 'structural axis weight. 0 = pure-semantic scoring',
    step: 0.05,
  },
  {
    key: 'radius_size',
    label: 'radius size',
    hint: 'neighbors included around each selected message',
    step: 1,
    integer: true,
  },
  {
    key: 'rerank_top_k',
    label: 'rerank top-k',
    hint: 'candidates scored by the reranker per new message',
    step: 1,
    integer: true,
  },
]

function parse<T>(raw: string | undefined | null, fallback: T): T {
  if (!raw) return fallback
  try {
    const v = JSON.parse(raw)
    // Backend sometimes serializes empty slices/maps as the literal string
    // "null" (e.g. settings.hooks when no hooks configured). JSON.parse
    // yields JavaScript null — which would crash any .length / .map on
    // the caller side. Fall back to the typed default instead.
    if (v === null || v === undefined) return fallback
    return v as T
  } catch {
    return fallback
  }
}

const EMPTY: ProviderConfig = { adapter: '', model: '', base_url: '' }

export function Settings() {
  const { data, loading, error } = useQuery<{
    settings: {
      providers: string
      preferences: string
      permissions: string
      mcpServers: string
      hooks: string
      engine: string
    }
  }>(GET_SETTINGS)
  const [updateSettings, { loading: saving, error: saveError }] = useMutation(UPDATE_SETTINGS)

  const [providers, setProviders] = useState<ProvidersMap>({})
  const [prefs, setPrefs] = useState<Preferences>({})
  const [permissions, setPermissions] = useState<PermissionsMap>({})
  const [mcpServers, setMcpServers] = useState<MCPServer[]>([])
  const [hooks, setHooks] = useState<HookConfig[]>([])
  const [engine, setEngine] = useState<EngineConfig>(ENGINE_DEFAULT)
  const [savedAt, setSavedAt] = useState<number | null>(null)

  useEffect(() => {
    if (!data?.settings) return
    setProviders(parse<ProvidersMap>(data.settings.providers, {}))
    setPrefs(parse<Preferences>(data.settings.preferences, {}))
    setPermissions(parse<PermissionsMap>(data.settings.permissions, {}))
    setMcpServers(parse<MCPServer[]>(data.settings.mcpServers, []))
    setHooks(parse<HookConfig[]>(data.settings.hooks, []))
    setEngine(parse<EngineConfig>(data.settings.engine, ENGINE_DEFAULT))
  }, [data])

  // Auto-clear saved indicator — kept above early returns so the hook order
  // stays stable regardless of loading/error states.
  useEffect(() => {
    if (savedAt === null) return
    const t = setTimeout(() => setSavedAt(null), 2000)
    return () => clearTimeout(t)
  }, [savedAt])

  if (loading) {
    return (
      <div className="firstrun">
        <div className="lede">Loading settings…</div>
      </div>
    )
  }
  if (error) {
    return (
      <div className="firstrun">
        <div className="lede" style={{ color: 'var(--danger)' }}>
          Failed to load settings: {error.message}
        </div>
      </div>
    )
  }

  function setKey(key: string, patch: Partial<ProviderConfig>) {
    setProviders((cur) => ({
      ...cur,
      [key]: { ...EMPTY, ...(cur[key] || {}), ...patch },
    }))
  }

  async function save() {
    const pruned: ProvidersMap = {}
    for (const [k, v] of Object.entries(providers)) {
      if (v && (v.adapter || v.model || v.base_url)) pruned[k] = v
    }
    await updateSettings({
      variables: {
        input: {
          providers: JSON.stringify(pruned),
          preferences: JSON.stringify(prefs),
          permissions: JSON.stringify(permissions),
          mcpServers: JSON.stringify(mcpServers.filter((s) => s.name || s.endpoint)),
          hooks: JSON.stringify(hooks.filter((h) => h.event && h.command)),
          engine: JSON.stringify(engine),
        },
      },
    })
    setSavedAt(Date.now())
  }

  const main = providers.main ?? EMPTY
  const classifier = providers.classifier ?? EMPTY
  const embedder = providers.embedder ?? EMPTY
  const search = providers.search ?? EMPTY

  return (
    <div className="firstrun">
      <h1 style={{ marginTop: 32 }}>Settings.</h1>
      <div className="lede">
        Changes take effect on the next turn. Provider roles are persisted together — editing one
        preserves the rest.
      </div>

      <div className="role-card">
        <h3>Profile</h3>
        <div className="role-name">Name in system prompt</div>
        <div className="row">
          <label>name</label>
          <input
            value={prefs.name ?? ''}
            onChange={(e) => setPrefs({ ...prefs, name: e.target.value })}
            placeholder="Your name"
          />
        </div>
      </div>

      <RoleCard
        title="Main · your LLM"
        subtitle="Reasoning, tool use, responses"
        role={main}
        onChange={(p) => setKey('main', p)}
      />
      <RoleCard
        title="Classifier · RRC cross-encoder"
        subtitle="Pairwise dependency scoring (NLI)"
        role={classifier}
        onChange={(p) => setKey('classifier', p)}
      />
      <RoleCard
        title="Embedder · RRC embeddings"
        subtitle="Semantic similarity, corpus search"
        role={embedder}
        onChange={(p) => setKey('embedder', p)}
      />
      <RoleCard
        title="Search · web search provider"
        subtitle="Powers the agent's WebSearch tool"
        role={search}
        onChange={(p) => setKey('search', p)}
      />

      <div className="role-card">
        <h3>MCP servers</h3>
        <div className="role-name">
          Model Context Protocol endpoints that provide extra tools to the agent.
        </div>
        <div style={{ marginTop: 10, display: 'grid', gap: 8 }}>
          {mcpServers.length === 0 && (
            <div style={{ fontSize: 12, color: 'var(--muted)', fontFamily: 'var(--mono)' }}>
              no MCP servers configured
            </div>
          )}
          {mcpServers.map((s, i) => (
            <div
              key={i}
              style={{
                display: 'grid',
                gridTemplateColumns: '140px 1fr 70px auto',
                gap: 8,
                alignItems: 'center',
              }}
            >
              <input
                value={s.name}
                onChange={(e) => {
                  const next = mcpServers.slice()
                  next[i] = { ...s, name: e.target.value }
                  setMcpServers(next)
                }}
                placeholder="name"
                style={{
                  padding: '4px 8px',
                  fontFamily: 'var(--mono)',
                  fontSize: 12,
                  border: '1px solid var(--rule)',
                  borderRadius: 3,
                }}
              />
              <input
                value={s.endpoint}
                onChange={(e) => {
                  const next = mcpServers.slice()
                  next[i] = { ...s, endpoint: e.target.value }
                  setMcpServers(next)
                }}
                placeholder="http://host:port or stdio:command"
                style={{
                  padding: '4px 8px',
                  fontFamily: 'var(--mono)',
                  fontSize: 12,
                  border: '1px solid var(--rule)',
                  borderRadius: 3,
                }}
              />
              <label
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 5,
                  fontFamily: 'var(--mono)',
                  fontSize: 11,
                  color: 'var(--muted)',
                }}
              >
                <input
                  type="checkbox"
                  checked={s.enabled}
                  onChange={(e) => {
                    const next = mcpServers.slice()
                    next[i] = { ...s, enabled: e.target.checked }
                    setMcpServers(next)
                  }}
                />
                on
              </label>
              <button
                onClick={() => setMcpServers(mcpServers.filter((_, j) => j !== i))}
                title="remove"
                style={{ color: 'var(--danger)', padding: '2px 6px' }}
              >
                ×
              </button>
            </div>
          ))}
          <button
            onClick={() =>
              setMcpServers([...mcpServers, { name: '', endpoint: '', enabled: true }])
            }
            style={{
              padding: '4px 10px',
              border: '1px dashed var(--rule)',
              borderRadius: 3,
              fontFamily: 'var(--mono)',
              fontSize: 11,
              color: 'var(--muted)',
              alignSelf: 'start',
              marginTop: 4,
            }}
          >
            + add server
          </button>
        </div>
      </div>

      <div className="role-card">
        <h3>Lifecycle hooks</h3>
        <div className="role-name">
          Shell commands fired on agent lifecycle events. Hook failure fails closed.
        </div>
        <div style={{ marginTop: 8, display: 'flex', flexDirection: 'column', gap: 6 }}>
          {hooks.map((h, i) => (
            <div
              key={i}
              style={{
                display: 'grid',
                gridTemplateColumns: '140px 1fr 120px 80px auto',
                gap: 6,
                alignItems: 'center',
              }}
            >
              <select
                value={h.event}
                onChange={(e) => {
                  const next = hooks.slice()
                  next[i] = { ...h, event: e.target.value }
                  setHooks(next)
                }}
                style={{
                  padding: '4px 6px',
                  fontFamily: 'var(--mono)',
                  fontSize: 12,
                  border: '1px solid var(--rule)',
                  borderRadius: 3,
                }}
              >
                <option value="">— event —</option>
                {HOOK_EVENTS.map((ev) => (
                  <option key={ev} value={ev}>
                    {ev}
                  </option>
                ))}
              </select>
              <input
                value={h.command}
                onChange={(e) => {
                  const next = hooks.slice()
                  next[i] = { ...h, command: e.target.value }
                  setHooks(next)
                }}
                placeholder="shell command"
                style={{
                  padding: '4px 8px',
                  fontFamily: 'var(--mono)',
                  fontSize: 12,
                  border: '1px solid var(--rule)',
                  borderRadius: 3,
                }}
              />
              <input
                value={h.match}
                onChange={(e) => {
                  const next = hooks.slice()
                  next[i] = { ...h, match: e.target.value }
                  setHooks(next)
                }}
                placeholder="match (glob)"
                style={{
                  padding: '4px 8px',
                  fontFamily: 'var(--mono)',
                  fontSize: 12,
                  border: '1px solid var(--rule)',
                  borderRadius: 3,
                }}
              />
              <input
                value={h.timeout}
                onChange={(e) => {
                  const next = hooks.slice()
                  next[i] = { ...h, timeout: e.target.value }
                  setHooks(next)
                }}
                placeholder="10s"
                style={{
                  padding: '4px 8px',
                  fontFamily: 'var(--mono)',
                  fontSize: 12,
                  border: '1px solid var(--rule)',
                  borderRadius: 3,
                }}
              />
              <button
                onClick={() => setHooks(hooks.filter((_, j) => j !== i))}
                title="remove"
                style={{ color: 'var(--danger)', padding: '2px 6px' }}
              >
                ×
              </button>
            </div>
          ))}
          <button
            onClick={() =>
              setHooks([...hooks, { event: '', command: '', match: '', timeout: '10s' }])
            }
            style={{
              padding: '4px 10px',
              border: '1px dashed var(--rule)',
              borderRadius: 3,
              fontFamily: 'var(--mono)',
              fontSize: 11,
              color: 'var(--muted)',
              alignSelf: 'start',
              marginTop: 4,
            }}
          >
            + add hook
          </button>
        </div>
      </div>

      <div className="role-card" style={{ paddingBottom: 8 }}>
        <h3>Tool permissions</h3>
        <div className="role-name">
          Control whether each tool runs freely, asks first, or is blocked.
        </div>
        <div style={{ marginTop: 10, display: 'grid', gap: 4 }}>
          {KNOWN_TOOLS.map((t) => {
            const cur = permissions[t.name] ?? t.defaultPerm
            return (
              <div
                key={t.name}
                style={{
                  display: 'grid',
                  gridTemplateColumns: '130px 1fr auto',
                  gap: 10,
                  alignItems: 'center',
                  padding: '4px 0',
                  borderBottom: '1px dashed var(--rule-2)',
                }}
              >
                <span style={{ fontFamily: 'var(--mono)', fontSize: 12, color: 'var(--ink)' }}>
                  {t.name}
                </span>
                <span style={{ fontSize: 12, color: 'var(--muted)' }}>{t.desc}</span>
                <span
                  className="seg"
                  style={{
                    display: 'inline-flex',
                    border: '1px solid var(--rule)',
                    borderRadius: 3,
                    overflow: 'hidden',
                  }}
                >
                  {(['allow', 'ask', 'deny'] as Perm[]).map((p) => (
                    <button
                      key={p}
                      onClick={() => setPermissions({ ...permissions, [t.name]: p })}
                      style={{
                        padding: '3px 9px',
                        fontFamily: 'var(--mono)',
                        fontSize: 10.5,
                        background: cur === p ? 'var(--ink)' : 'transparent',
                        color: cur === p ? 'var(--paper)' : 'var(--muted)',
                        borderRight: p !== 'deny' ? '1px solid var(--rule)' : 'none',
                      }}
                    >
                      {p}
                    </button>
                  ))}
                </span>
              </div>
            )
          })}
        </div>
      </div>

      <div className="role-card">
        <h3>RRC engine</h3>
        <div className="role-name">
          Live-tunable retrieval parameters. Stored edges keep their raw reranker
          and temporal scores; the fused score and threshold are reprojected under
          the current config at walk time. Saves apply retroactively — the next
          walk sees the full edge history under the new weights, not just edges
          created afterward.
        </div>
        <div style={{ marginTop: 10, display: 'grid', gap: 8 }}>
          {ENGINE_FIELDS.map((f) => (
            <div
              key={f.key}
              style={{
                display: 'grid',
                gridTemplateColumns: '150px 110px 1fr',
                gap: 10,
                alignItems: 'center',
              }}
            >
              <span style={{ fontFamily: 'var(--mono)', fontSize: 12, color: 'var(--ink)' }}>
                {f.label}
              </span>
              <input
                type="number"
                min={0}
                step={f.step}
                value={engine[f.key]}
                onChange={(e) => {
                  const raw = e.target.value
                  const n = raw === '' ? 0 : f.integer ? parseInt(raw, 10) : parseFloat(raw)
                  if (Number.isNaN(n) || n < 0) return
                  setEngine({ ...engine, [f.key]: n })
                }}
                style={{
                  padding: '4px 8px',
                  fontFamily: 'var(--mono)',
                  fontSize: 12,
                  border: '1px solid var(--rule)',
                  borderRadius: 3,
                  textAlign: 'right',
                }}
              />
              <span style={{ fontSize: 12, color: 'var(--muted)' }}>{f.hint}</span>
            </div>
          ))}
          <button
            onClick={() => setEngine(ENGINE_DEFAULT)}
            style={{
              padding: '4px 10px',
              border: '1px dashed var(--rule)',
              borderRadius: 3,
              fontFamily: 'var(--mono)',
              fontSize: 11,
              color: 'var(--muted)',
              alignSelf: 'start',
              marginTop: 4,
            }}
          >
            reset to defaults
          </button>
        </div>
      </div>

      <div style={{ display: 'flex', gap: 12, marginTop: 24, alignItems: 'center' }}>
        <button
          onClick={() => void save()}
          disabled={saving}
          style={{
            padding: '9px 18px',
            background: 'var(--ink)',
            color: 'var(--paper)',
            borderRadius: 3,
            fontFamily: 'var(--mono)',
            fontSize: 12,
            letterSpacing: '0.04em',
            textTransform: 'uppercase',
            opacity: saving ? 0.5 : 1,
          }}
        >
          {saving ? 'saving…' : 'save'}
        </button>
        <div style={{ flex: 1 }} />
        {saveError && (
          <span style={{ fontFamily: 'var(--mono)', fontSize: 11, color: 'var(--danger)' }}>
            {saveError.message}
          </span>
        )}
        {savedAt && !saveError && (
          <span style={{ fontFamily: 'var(--mono)', fontSize: 11, color: 'var(--ok)' }}>saved</span>
        )}
      </div>
    </div>
  )
}

interface RoleCardProps {
  title: string
  subtitle: string
  role: ProviderConfig
  onChange: (patch: Partial<ProviderConfig>) => void
}

function RoleCard({ title, subtitle, role, onChange }: RoleCardProps) {
  return (
    <div className="role-card">
      <h3>{title}</h3>
      <div className="role-name">{subtitle}</div>
      <div className="row">
        <label>adapter</label>
        <input
          value={role.adapter}
          onChange={(e) => onChange({ adapter: e.target.value })}
          placeholder="ollama / openai / anthropic / tei / searxng"
        />
      </div>
      <div className="row">
        <label>model</label>
        <input value={role.model} onChange={(e) => onChange({ model: e.target.value })} />
      </div>
      <div className="row">
        <label>base url</label>
        <input
          value={role.base_url ?? ''}
          onChange={(e) => onChange({ base_url: e.target.value })}
          placeholder="http://localhost:…"
        />
      </div>
    </div>
  )
}
