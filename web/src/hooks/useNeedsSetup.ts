import { useSettings } from './useSettings'

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
  const { providers, loading, error } = useSettings()
  if (loading) return { needsSetup: null, error }
  const ok = !!(providers.main?.adapter && providers.main?.model)
  return { needsSetup: !ok, error }
}
