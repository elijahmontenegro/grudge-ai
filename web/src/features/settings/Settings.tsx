import { useEffect, useState } from 'react'
import { useMutation, useQuery } from '@apollo/client/react'
import { GET_SETTINGS, UPDATE_SETTINGS } from '@/graphql/operations'
import {
  ENGINE_DEFAULT,
  parseSetting,
  type EngineConfig,
  type HookConfig,
  type MCPServer,
  type PermissionsMap,
  type Preferences,
  type ProvidersMap,
} from './types'
import { General } from './General'
import { Providers } from './Providers'
import { Sandbox } from './Sandbox'
import { Tools } from './Tools'
import { Advanced } from './Advanced'
import { About } from './About'

/**
 * Settings shell. Owns the GET_SETTINGS query, the per-section
 * state slices, and the merged save. Each section is a presenter
 * that takes its slice + setter — easy to test in isolation, easy
 * to add new sections without touching siblings.
 */
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
    setProviders(parseSetting<ProvidersMap>(data.settings.providers, {}))
    setPrefs(parseSetting<Preferences>(data.settings.preferences, {}))
    setPermissions(parseSetting<PermissionsMap>(data.settings.permissions, {}))
    setMcpServers(parseSetting<MCPServer[]>(data.settings.mcpServers, []))
    setHooks(parseSetting<HookConfig[]>(data.settings.hooks, []))
    setEngine(parseSetting<EngineConfig>(data.settings.engine, ENGINE_DEFAULT))
  }, [data])

  // Auto-clear saved indicator — kept above early returns so the
  // hook order stays stable regardless of loading/error states.
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

  return (
    <div className="firstrun">
      <h1 style={{ marginTop: 32 }}>Settings.</h1>
      <div className="lede">
        Changes take effect on the next turn. Provider roles are persisted together — editing one
        preserves the rest.
      </div>

      <General prefs={prefs} setPrefs={setPrefs} />
      <Providers providers={providers} setProviders={setProviders} />
      <Sandbox />
      <Tools
        mcpServers={mcpServers}
        setMcpServers={setMcpServers}
        hooks={hooks}
        setHooks={setHooks}
        permissions={permissions}
        setPermissions={setPermissions}
      />
      <Advanced engine={engine} setEngine={setEngine} />
      <About />

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
