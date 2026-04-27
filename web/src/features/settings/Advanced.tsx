import { ENGINE_DEFAULT, type EngineConfig } from './types'

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

interface AdvancedProps {
  engine: EngineConfig
  setEngine: (next: EngineConfig) => void
}

/** RRC engine knobs — live-tunable. Backend recomputes edge
 *  scores under the new config at walk time, so saves apply
 *  retroactively. */
export function Advanced({ engine, setEngine }: AdvancedProps) {
  return (
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
  )
}
