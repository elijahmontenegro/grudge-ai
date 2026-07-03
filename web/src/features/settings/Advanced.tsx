import { ENGINE_DEFAULT, type EngineConfig } from './types'

// The retired flat-threshold knobs (edge_threshold, score_floor,
// z_score_threshold) are gone from EngineConfig entirely — the calibrated
// acceptance model replaced them, and the wire carries only live keys.
// loss_ratio is their replacement: the one honest hand-set knob.
const ENGINE_FIELDS: Array<{
  key: keyof EngineConfig
  label: string
  hint: string
  step: number
  integer?: boolean
}> = [
  {
    key: 'loss_ratio',
    label: 'precision stance',
    hint: 'calibrated P(prerequisite) a candidate must clear. higher = stricter, more abstention; lower = more recall, more junk risk',
    step: 0.05,
  },
  {
    key: 'min_batch_stddev',
    label: 'minimum batch spread',
    hint: 'return no edges when scorer outputs are too flat',
    step: 0.05,
  },
  {
    key: 'local_context_size',
    label: 'local context size',
    hint: 'recent same-thread messages used for prerequisite scoring and the model payload',
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
  {
    key: 'context_budget_tokens',
    label: 'context budget',
    hint: 'estimated input-token ceiling before provider headroom',
    step: 1000,
    integer: true,
  },
  {
    key: 'diversity_lambda',
    label: 'diversity lambda',
    hint: 'MMR relevance weight in the range 0 to 1',
    step: 0.05,
  },
  {
    key: 'budget_headroom_pct',
    label: 'budget headroom',
    hint: 'fraction of the context budget available to assembly',
    step: 0.05,
  },
  {
    key: 'per_msg_delimiter_tokens',
    label: 'message overhead',
    hint: 'estimated provider delimiter tokens per message',
    step: 1,
    integer: true,
  },
]

interface AdvancedProps {
  engine: EngineConfig
  setEngine: (next: EngineConfig) => void
}

/** Live RRC engine controls. */
export function Advanced({ engine, setEngine }: AdvancedProps) {
  return (
    <div className="role-card">
      <h3>RRC engine</h3>
      <div className="role-name">
        Retrieval, Local Context, and payload-budget parameters.
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
  )
}
