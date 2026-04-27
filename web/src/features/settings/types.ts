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

// Matches service/config/settings.go:EngineConfig. Tunable at
// runtime; backend recomputes edge scores under the new config at
// walk time, so edits take effect on the very next turn with no
// rebuild.
export interface EngineConfig {
  edge_threshold: number
  score_floor: number
  weight_ce: number
  weight_temp: number
  radius_size: number
  rerank_top_k: number
}

export const ENGINE_DEFAULT: EngineConfig = {
  edge_threshold: 0.35,
  score_floor: 0.01,
  weight_ce: 0.6,
  weight_temp: 0.4,
  radius_size: 10,
  rerank_top_k: 64,
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
