// Shared types used across the settings sections. Keys match the
// service/config/settings.go backend schema exactly. Writing with
// different keys would wipe the existing config on save because
// UpdateSettings.Providers is a full replace, not merge.

export interface ProviderConfig {
  adapter: string
  model: string
  base_url?: string
  api_key?: string
}

export type ProvidersMap = Record<string, ProviderConfig>

export interface Preferences {
  name?: string
  [k: string]: unknown
}

export type Perm = 'allow' | 'ask' | 'deny'
export type PermissionsMap = Record<string, Perm>

export interface MCPServer {
  name: string
  endpoint: string
  enabled: boolean
}

export interface HookConfig {
  event: string
  command: string
  match: string
  timeout: string
}

// Matches service/config/settings.go:EngineConfig key-for-key.
// Tunable at runtime; saves apply to the live engine with no rebuild.
//
// loss_ratio is the acceptance operating point — the precision stance
// (a candidate is accepted when its calibrated P(prerequisite) clears
// it). It is the one hand-set knob; the calibrator coefficients behind
// it are learned automatically per scorer.
export interface EngineConfig {
  loss_ratio: number
  min_batch_stddev: number
  local_context_size: number
  rerank_top_k: number
  context_budget_tokens: number
  diversity_lambda: number
  budget_headroom_pct: number
  per_msg_delimiter_tokens: number
}

export const ENGINE_DEFAULT: EngineConfig = {
  loss_ratio: 0.5,
  min_batch_stddev: 0.05,
  local_context_size: 10,
  rerank_top_k: 64,
  context_budget_tokens: 150000,
  diversity_lambda: 0.7,
  budget_headroom_pct: 0.9,
  per_msg_delimiter_tokens: 5,
}

export const EMPTY_PROVIDER: ProviderConfig = { adapter: '', model: '', base_url: '' }

export function parseSetting<T>(raw: string | undefined | null, fallback: T): T {
  if (!raw) return fallback
  try {
    const v = JSON.parse(raw)
    // Backend sometimes serializes empty slices/maps as the literal
    // string "null" (e.g. settings.hooks when no hooks configured).
    // JSON.parse yields JavaScript null — which would crash any
    // .length / .map on the caller side. Fall back to the typed
    // default instead.
    if (v === null || v === undefined) return fallback
    return v as T
  } catch {
    return fallback
  }
}
