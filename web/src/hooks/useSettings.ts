import { useMemo } from 'react'
import { useQuery } from '@apollo/client/react'
import { GET_SETTINGS } from '@/graphql/operations'
import type { GetSettingsQuery } from '@/graphql/generated/types'

export interface ProviderConfig {
  adapter?: string
  model?: string
  base_url?: string
  api_key?: string
}

export type ProvidersMap = Record<string, ProviderConfig>

export interface Preferences {
  name?: string
  [k: string]: unknown
}

export interface ParsedSettings {
  providers: ProvidersMap
  preferences: Preferences
  loading: boolean
  error: string | null
}

function parse<T>(raw: string | undefined | null, fallback: T): T {
  if (!raw) return fallback
  try {
    const v = JSON.parse(raw)
    if (v === null || v === undefined) return fallback
    return v as T
  } catch {
    return fallback
  }
}

/**
 * Single source of truth for parsed settings. Apollo dedupes the
 * underlying GET_SETTINGS query across consumers, but each parse
 * was its own try/catch boilerplate before — useMe stamping out
 * preferences, useNeedsSetup stamping out providers, both reaching
 * into the same blob with the same JSON-parse pattern.
 */
export function useSettings(): ParsedSettings {
  const { data, loading, error } = useQuery<GetSettingsQuery>(GET_SETTINGS)
  return useMemo(() => {
    return {
      providers: parse<ProvidersMap>(data?.settings?.providers, {}),
      preferences: parse<Preferences>(data?.settings?.preferences, {}),
      loading,
      error: error?.message ?? null,
    }
  }, [data, loading, error])
}
