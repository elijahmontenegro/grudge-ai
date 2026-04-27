import { Form } from '@/primitives/Form'
import { EMPTY_PROVIDER, type ProviderConfig, type ProvidersMap } from './types'

interface ProvidersProps {
  providers: ProvidersMap
  setProviders: (next: ProvidersMap) => void
}

/** The four provider roles Spidey speaks to. Editing one preserves
 *  the rest because UpdateSettings.Providers is a full replace —
 *  the shell merges every role into the same map before save. */
export function Providers({ providers, setProviders }: ProvidersProps) {
  function setKey(key: string, patch: Partial<ProviderConfig>) {
    setProviders({
      ...providers,
      [key]: { ...EMPTY_PROVIDER, ...(providers[key] || {}), ...patch },
    })
  }

  const main = providers.main ?? EMPTY_PROVIDER
  const classifier = providers.classifier ?? EMPTY_PROVIDER
  const embedder = providers.embedder ?? EMPTY_PROVIDER
  const search = providers.search ?? EMPTY_PROVIDER

  return (
    <>
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
    </>
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
      <Form.Row label="adapter">
        <input
          value={role.adapter}
          onChange={(e) => onChange({ adapter: e.target.value })}
          placeholder="ollama / openai / anthropic / tei / searxng"
        />
      </Form.Row>
      <Form.Row label="model">
        <input value={role.model} onChange={(e) => onChange({ model: e.target.value })} />
      </Form.Row>
      <Form.Row label="base url">
        <input
          value={role.base_url ?? ''}
          onChange={(e) => onChange({ base_url: e.target.value })}
          placeholder="http://localhost:…"
        />
      </Form.Row>
    </div>
  )
}
