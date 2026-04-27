import { SegButton } from '@/primitives/SegButton'
import type {
  HookConfig,
  MCPServer,
  Perm,
  PermissionsMap,
} from './types'

const HOOK_EVENTS = [
  'before_tool',
  'after_tool',
  'before_send',
  'after_receive',
  'on_start',
  'on_stop',
]

// The tool set the backend enforces permissions on
// (service/agent/tools.go). Ordered by risk so destructive /
// ask-gated tools sit at the top.
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

interface ToolsProps {
  mcpServers: MCPServer[]
  setMcpServers: (next: MCPServer[]) => void
  hooks: HookConfig[]
  setHooks: (next: HookConfig[]) => void
  permissions: PermissionsMap
  setPermissions: (next: PermissionsMap) => void
}

/** Tool-related settings: MCP server list, lifecycle hooks, and
 *  per-tool permission gates. */
export function Tools({
  mcpServers,
  setMcpServers,
  hooks,
  setHooks,
  permissions,
  setPermissions,
}: ToolsProps) {
  return (
    <>
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
                <SegButton
                  className="perm-select"
                  value={cur}
                  onChange={(next) => setPermissions({ ...permissions, [t.name]: next })}
                  options={[
                    { value: 'allow', label: 'allow' },
                    { value: 'ask', label: 'ask' },
                    { value: 'deny', label: 'deny' },
                  ]}
                />
              </div>
            )
          })}
        </div>
      </div>
    </>
  )
}
