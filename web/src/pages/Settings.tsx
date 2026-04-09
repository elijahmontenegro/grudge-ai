import { useState, useEffect } from 'react'
import { gql } from '@apollo/client'
import { useQuery, useMutation } from '@apollo/client/react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ScrollArea } from '@/components/ui/scroll-area'
import { cn } from '@/lib/utils'
import { ThreadSidebar } from '@/components/organisms/ThreadSidebar'

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
  api_key?: string
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
    description: 'Primary LLM for reasoning, tool use, and responses.',
    placeholder: 'claude-sonnet-4-20250514',
    adapters: ['anthropic', 'openai', 'googleai', 'ollama', 'vllm'],
  },
  {
    key: 'classifier',
    label: 'Cross-Encoder (RRC)',
    description: 'NLI model for prerequisite detection. Runs locally via TEI.',
    placeholder: 'cross-encoder/nli-deberta-v3-base',
    adapters: ['tei'],
  },
  {
    key: 'embedder',
    label: 'Embedder (RRC + Search)',
    description: 'Embedding model for similarity scoring and semantic search.',
    placeholder: 'BAAI/bge-small-en-v1.5',
    adapters: ['tei', 'openai'],
  },
  {
    key: 'small_fast',
    label: 'Small Fast Model',
    description: 'QUD extraction, autocomplete, annotations.',
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
    <div className="space-y-3 p-4 rounded-xl bg-card border border-border">
      <div>
        <h3 className="text-sm font-medium">{role.label}</h3>
        <p className="text-xs text-muted-foreground mt-0.5">{role.description}</p>
      </div>

      <div className="grid grid-cols-3 gap-2">
        <div>
          <label className="text-[11px] text-muted-foreground block mb-1">Provider</label>
          <select
            value={config.adapter}
            onChange={(e) => {
              const adapter = ADAPTERS.find((a) => a.value === e.target.value)
              onChange({
                adapter: e.target.value,
                model: config.model,
                base_url: config.base_url || adapter?.defaultURL || '',
              })
            }}
            className="w-full border border-input rounded-md px-2 py-1.5 text-sm bg-background"
          >
            <option value="">Select...</option>
            {availableAdapters.map((a) => (
              <option key={a.value} value={a.value}>{a.label}</option>
            ))}
          </select>
        </div>

        <div>
          <label className="text-[11px] text-muted-foreground block mb-1">Model</label>
          <Input
            value={config.model}
            onChange={(e) => onChange({ ...config, model: e.target.value })}
            placeholder={role.placeholder}
            className="text-sm"
          />
        </div>

        <div>
          <label className="text-[11px] text-muted-foreground block mb-1">Endpoint</label>
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
          <label className="text-[11px] text-muted-foreground block mb-1">
            API Key
            <span className="text-muted-foreground/50 ml-1">stored in OS keychain</span>
          </label>
          <Input
            type="password"
            value={config.api_key ?? ''}
            onChange={(e) => onChange({ ...config, api_key: e.target.value })}
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

  if (loading) return <div className="p-8 text-muted-foreground">Loading settings...</div>

  const allConfigured = ROLES.every((role) => {
    const p = providers[role.key]
    return p?.adapter && p?.model
  })

  return (
    <div className="flex h-screen">
      <ThreadSidebar />
      <ScrollArea className="flex-1 min-w-0">
        <div className="max-w-2xl mx-auto p-8 space-y-6">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Settings</h1>
          <p className="text-sm text-muted-foreground mt-1">
            Configure model providers. All roles are required for RRC.
          </p>
        </div>

        {!allConfigured && (
          <div className={cn(
            'rounded-xl p-4 text-sm border',
            'border-amber-500/20 bg-amber-500/5 text-amber-200'
          )}>
            <p className="font-medium text-xs">Providers not fully configured</p>
            <p className="text-xs text-muted-foreground mt-0.5">Configure all roles below to start using Spidey.</p>
          </div>
        )}

        <div className="space-y-3">
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
          {saved && <span className="text-sm text-emerald-500/70">Settings applied.</span>}
        </div>

        <details className="text-xs text-muted-foreground">
          <summary className="cursor-pointer font-medium text-sm hover:text-foreground transition-colors">
            Cross-encoder setup (Docker)
          </summary>
          <div className="mt-3 space-y-2">
            <p>The RRC cross-encoder requires TEI serving DeBERTa-v3 NLI locally:</p>
            <pre className="bg-secondary/50 p-3 rounded-lg font-mono text-xs overflow-x-auto">
docker run -p 8080:80 ghcr.io/huggingface/text-embeddings-inference:latest \
  --model-id cross-encoder/nli-deberta-v3-base</pre>
            <p>CPU inference. ~184MB model download. No GPU required.</p>
          </div>
        </details>
      </div>
      </ScrollArea>
    </div>
  )
}
