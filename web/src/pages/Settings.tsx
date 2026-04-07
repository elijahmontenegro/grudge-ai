import { useState, useEffect } from 'react'
import { gql } from '@apollo/client'
import { useQuery, useMutation } from '@apollo/client/react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Separator } from '@/components/ui/separator'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Badge } from '@/components/ui/badge'

const SETTINGS_QUERY = gql`
  query Settings {
    settings {
      providers
      permissions
      preferences
    }
  }
`

const UPDATE_SETTINGS = gql`
  mutation UpdateSettings($input: SettingsInput!) {
    updateSettings(input: $input) {
      providers
      permissions
      preferences
    }
  }
`

interface ProviderConfig {
  adapter: string
  model: string
  base_url: string
}

const ADAPTERS = [
  { value: 'anthropic', label: 'Anthropic', needsKey: true, defaultURL: 'https://api.anthropic.com' },
  { value: 'openai', label: 'OpenAI', needsKey: true, defaultURL: 'https://api.openai.com' },
  { value: 'googleai', label: 'Google AI', needsKey: true, defaultURL: '' },
  { value: 'ollama', label: 'Ollama (local)', needsKey: false, defaultURL: 'http://localhost:11434' },
  { value: 'vllm', label: 'vLLM', needsKey: false, defaultURL: '' },
  { value: 'tei', label: 'TEI (Docker)', needsKey: false, defaultURL: 'http://localhost:8080' },
]

const ROLES = [
  {
    key: 'main',
    label: 'Main Model',
    description: 'Your primary LLM for reasoning, tool use, and responses.',
    placeholder: 'claude-sonnet-4-20250514',
    adapters: ['anthropic', 'openai', 'googleai', 'ollama', 'vllm'],
  },
  {
    key: 'classifier',
    label: 'Cross-Encoder (RRC)',
    description: 'NLI model for prerequisite detection. Runs locally via TEI in Docker — no GPU required.',
    placeholder: 'cross-encoder/nli-deberta-v3-base',
    adapters: ['tei'],
  },
  {
    key: 'embedder',
    label: 'Embedder (RRC + Search)',
    description: 'Embedding model for similarity scoring and semantic search. Combined with NLI classifier for dependency detection.',
    placeholder: 'BAAI/bge-small-en-v1.5',
    adapters: ['tei', 'openai'],
  },
  {
    key: 'small_fast',
    label: 'Small Fast Model',
    description: 'QUD extraction, autocomplete, annotations. Sub-second latency preferred.',
    placeholder: 'claude-haiku-4-5-20251001',
    adapters: ['anthropic', 'openai', 'googleai', 'ollama', 'vllm'],
  },
]

function ProviderForm({
  role,
  config,
  onChange,
}: {
  role: typeof ROLES[0]
  config: ProviderConfig
  onChange: (config: ProviderConfig) => void
}) {
  const availableAdapters = ADAPTERS.filter((a) => role.adapters.includes(a.value))
  const selectedAdapter = ADAPTERS.find((a) => a.value === config.adapter)

  return (
    <div className="space-y-3 p-4 border border-border rounded-lg">
      <div>
        <h3 className="text-sm font-semibold">{role.label}</h3>
        <p className="text-xs text-muted">{role.description}</p>
      </div>

      <div className="grid grid-cols-3 gap-2">
        <div>
          <label className="text-xs text-muted block mb-1">Provider</label>
          <select
            value={config.adapter}
            onChange={(e) => {
              const adapter = ADAPTERS.find((a) => a.value === e.target.value)
              onChange({
                adapter: e.target.value,
                model: config.model,
                base_url: adapter?.defaultURL ?? config.base_url,
              })
            }}
            className="w-full border border-border rounded-md px-2 py-1.5 text-sm bg-background"
          >
            <option value="">Select...</option>
            {availableAdapters.map((a) => (
              <option key={a.value} value={a.value}>{a.label}</option>
            ))}
          </select>
        </div>

        <div>
          <label className="text-xs text-muted block mb-1">Model</label>
          <Input
            value={config.model}
            onChange={(e) => onChange({ ...config, model: e.target.value })}
            placeholder={role.placeholder}
            className="text-sm"
          />
        </div>

        <div>
          <label className="text-xs text-muted block mb-1">Endpoint</label>
          <Input
            value={config.base_url}
            onChange={(e) => onChange({ ...config, base_url: e.target.value })}
            placeholder={selectedAdapter?.defaultURL || 'https://...'}
            className="text-sm"
          />
        </div>
      </div>

      {selectedAdapter?.needsKey && (
        <div>
          <label className="text-xs text-muted block mb-1">
            API Key <Badge variant="secondary" className="text-[10px] ml-1">stored in OS keychain</Badge>
          </label>
          <Input
            type="password"
            placeholder="sk-..."
            className="text-sm"
          />
        </div>
      )}
    </div>
  )
}

export function SettingsPage() {
  type SettingsData = { settings: { providers: string; permissions: string; preferences: string } }
  const { data, loading, refetch } = useQuery<SettingsData>(SETTINGS_QUERY)
  const [updateSettings] = useMutation<{ updateSettings: SettingsData['settings'] }>(UPDATE_SETTINGS)
  const [providers, setProviders] = useState<Record<string, ProviderConfig>>({})
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    if (data?.settings?.providers) {
      try {
        setProviders(JSON.parse(data.settings.providers))
      } catch {
        setProviders({})
      }
    }
  }, [data])

  const handleSave = async () => {
    setSaving(true)
    setSaved(false)
    await updateSettings({
      variables: {
        input: { providers: JSON.stringify(providers) },
      },
    })
    await refetch()
    setSaving(false)
    setSaved(true)
  }

  if (loading) return <div className="p-8 text-muted">Loading settings...</div>

  const allConfigured = ROLES.every((role) => {
    const p = providers[role.key]
    return p?.adapter && p?.model
  })

  return (
    <ScrollArea className="h-screen">
      <div className="max-w-2xl mx-auto p-8 space-y-6">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Spidey Settings</h1>
          <p className="text-sm text-muted mt-1">
            All three model roles are required. RRC has no degraded mode.
          </p>
        </div>

        {!allConfigured && (
          <div className="bg-destructive/10 border border-destructive/20 rounded-lg p-4">
            <p className="text-sm font-medium">Providers not fully configured</p>
            <p className="text-xs text-muted mt-1">Configure all three roles below to start using Spidey.</p>
          </div>
        )}

        <Separator />

        <div className="space-y-4">
          {ROLES.map((role) => (
            <ProviderForm
              key={role.key}
              role={role}
              config={providers[role.key] || { adapter: '', model: '', base_url: '' }}
              onChange={(config) => setProviders((prev) => ({ ...prev, [role.key]: config }))}
            />
          ))}
        </div>

        <div className="flex items-center gap-3">
          <Button onClick={handleSave} disabled={saving}>
            {saving ? 'Saving...' : 'Save'}
          </Button>
          {saved && <span className="text-sm text-muted">Saved. Restart the service to apply.</span>}
        </div>

        <Separator />

        <details className="text-xs text-muted">
          <summary className="cursor-pointer font-medium text-sm">Cross-encoder setup (Docker)</summary>
          <div className="mt-2 space-y-2">
            <p>The RRC cross-encoder requires TEI serving DeBERTa-v3 NLI locally:</p>
            <pre className="bg-secondary p-3 rounded font-mono text-xs overflow-x-auto">
docker run -p 8080:80 ghcr.io/huggingface/text-embeddings-inference:latest \
  --model-id cross-encoder/nli-deberta-v3-base</pre>
            <p>CPU inference. ~184MB model download. No GPU required.</p>
          </div>
        </details>
      </div>
    </ScrollArea>
  )
}
