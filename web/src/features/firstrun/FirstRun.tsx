import { useEffect, useState } from 'react'
import { useMutation, useQuery } from '@apollo/client/react'
import { GET_SETTINGS, UPDATE_SETTINGS } from '@/graphql/operations'
import { Form } from '@/primitives/Form'
import type {
  GetSettingsQuery,
  UpdateSettingsMutation,
  UpdateSettingsMutationVariables,
} from '@/graphql/generated/types'

interface ProviderConfig {
  adapter: string
  model: string
  base_url?: string
  api_key?: string
}

type ProvidersMap = Record<string, ProviderConfig>

interface Preferences {
  name?: string
}

function parse<T>(raw: string | undefined | null, fallback: T): T {
  if (!raw) return fallback
  try {
    const v = JSON.parse(raw)
    // Backend sometimes serializes empty slices/maps as literal "null"
    // (e.g. unset preferences). Substitute the typed fallback so callers
    // can safely destructure / iterate.
    if (v === null || v === undefined) return fallback
    return v as T
  } catch {
    return fallback
  }
}

// First-run focuses on the minimum to get the app usable: main model.
// Other providers (classifier, embedder, search) can be configured
// later in Settings. They are NOT auto-probed — the backend only
// initializes providers that appear in config.Settings.Providers.
export function FirstRun({ onComplete }: { onComplete: () => void }) {
  const { data } = useQuery<GetSettingsQuery>(GET_SETTINGS)
  const [save, { loading: saving, error: saveError }] = useMutation<
    UpdateSettingsMutation,
    UpdateSettingsMutationVariables
  >(UPDATE_SETTINGS)

  const [name, setName] = useState('')
  // Seed with sensible defaults for local Ollama; user overrides as needed.
  const [main, setMain] = useState<ProviderConfig>({
    adapter: 'ollama',
    model: 'llama3.1:8b-instruct',
    base_url: 'http://localhost:11434',
  })
  const [providers, setProviders] = useState<ProvidersMap>({})

  useEffect(() => {
    if (!data?.settings) return
    const ps = parse<ProvidersMap>(data.settings.providers, {})
    const prefs = parse<Preferences>(data.settings.preferences, {})
    setProviders(ps)
    if (prefs.name) setName(prefs.name)
    if (ps.main) setMain({ ...main, ...ps.main })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data])

  async function confirm() {
    // Preserve every other provider the backend already knows about —
    // updateSettings replaces the whole providers map, not merges.
    const merged: ProvidersMap = { ...providers, main }
    const preferences: Preferences = { name: name.trim() || undefined }
    await save({
      variables: {
        input: {
          providers: JSON.stringify(merged),
          preferences: JSON.stringify(preferences),
        },
      },
    })
    onComplete()
  }

  const canConfirm = !!main.adapter && !!main.model

  return (
    <div className="firstrun">
      <div className="firstrun-brand">
        <span className="firstrun-dot" />
        <span className="firstrun-wordmark">spidey</span>
        <span className="firstrun-sep">/</span>
        <span className="firstrun-crumb">first run</span>
      </div>
      <h1>Welcome to Spidey.</h1>
      <div className="lede">
        Spidey runs locally. Point it at a model provider to get started. RRC classifier,
        embedder, and web search are optional — configure them later in Settings if you want
        prerequisite selection, semantic search, or the WebSearch tool.
      </div>

      <div className="role-card">
        <h3>You</h3>
        <div className="role-name">Profile</div>
        <Form.Row label="Name">
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Your name"
          />
        </Form.Row>
      </div>

      <div className="role-card">
        <h3>Main model · your LLM</h3>
        <div className="role-name">Reasoning, tool use, responses</div>
        <Form.Row label="adapter">
          <input
            value={main.adapter}
            onChange={(e) => setMain({ ...main, adapter: e.target.value })}
            placeholder="ollama / openai / anthropic"
          />
        </Form.Row>
        <Form.Row label="model">
          <input value={main.model} onChange={(e) => setMain({ ...main, model: e.target.value })} />
        </Form.Row>
        <Form.Row label="base url">
          <input
            value={main.base_url ?? ''}
            onChange={(e) => setMain({ ...main, base_url: e.target.value })}
            placeholder="http://localhost:11434"
          />
        </Form.Row>
      </div>

      <div style={{ display: 'flex', gap: 12, marginTop: 24, alignItems: 'center' }}>
        <button
          onClick={() => void confirm()}
          disabled={saving || !canConfirm}
          style={{
            padding: '9px 18px',
            background: 'var(--ink)',
            color: 'var(--paper)',
            borderRadius: 3,
            fontFamily: 'var(--mono)',
            fontSize: 12,
            letterSpacing: '0.04em',
            textTransform: 'uppercase',
            opacity: saving || !canConfirm ? 0.5 : 1,
          }}
        >
          {saving ? 'saving…' : 'confirm · open spidey'}
        </button>
        <div style={{ flex: 1 }} />
        {saveError ? (
          <span style={{ fontFamily: 'var(--mono)', fontSize: 11, color: 'var(--danger)' }}>
            {saveError.message}
          </span>
        ) : (
          <div style={{ fontFamily: 'var(--mono)', fontSize: 11, color: 'var(--muted)' }}>
            no cloud · no multi-tenant · no degraded mode
          </div>
        )}
      </div>
    </div>
  )
}
