import { useQuery } from '@apollo/client/react'
import { GET_SETTINGS } from '@/graphql/operations'
import type { GetSettingsQuery } from '@/graphql/generated/types'

export interface NeedsSetupResult {
  /** null while loading — caller should hold routing decisions */
  needsSetup: boolean | null
  error: string | null
}

/**
 * Returns true iff the settings.providers blob has no viable `main` provider.
 * Used to redirect into the /firstrun flow on first open.
 */
export function useNeedsSetup(): NeedsSetupResult {
  const { data, loading, error } = useQuery<GetSettingsQuery>(GET_SETTINGS)
  if (loading || !data) return { needsSetup: null, error: error?.message ?? null }
  if (!data.settings?.providers) return { needsSetup: true, error: null }
  try {
    const parsed = JSON.parse(data.settings.providers)
    const providers = (parsed ?? {}) as {
      main?: { adapter?: string; model?: string }
    }
    const ok = !!(providers.main?.adapter && providers.main?.model)
    return { needsSetup: !ok, error: null }
  } catch {
    return { needsSetup: true, error: null }
  }
}
