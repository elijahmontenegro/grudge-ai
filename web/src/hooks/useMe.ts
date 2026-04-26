import { useMemo } from 'react'
import { useSettings } from './useSettings'
import { USER as FALLBACK } from '@/data/fixtures'
import type { User } from '@/domain/types'

/**
 * Reads the current user from settings.preferences.name. Falls back to the
 * fixture USER for missing fields so nothing goes blank during loading.
 * Derives initials from whichever name we end up with.
 */
export function useMe(): User {
  const { preferences } = useSettings()
  return useMemo<User>(() => {
    const name = preferences.name?.trim() || FALLBACK.name
    const parts = name.split(/\s+/).filter(Boolean)
    const initials = (parts[0]?.[0] ?? 'U') + (parts[1]?.[0] ?? '')
    return {
      name,
      handle: name.split(' ')[0]?.toLowerCase() ?? FALLBACK.handle,
      initials: initials.toUpperCase(),
      host: FALLBACK.host,
    }
  }, [preferences])
}

/** Time-of-day greeting — local hour, no backend required. */
export function useGreeting(): string {
  const h = new Date().getHours()
  if (h < 5) return 'Still up'
  if (h < 12) return 'Good morning'
  if (h < 18) return 'Good afternoon'
  return 'Good evening'
}
