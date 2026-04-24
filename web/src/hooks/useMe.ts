import { useMemo } from 'react'
import { useQuery } from '@apollo/client/react'
import { GET_SETTINGS } from '@/graphql/operations'
import { USER as FALLBACK } from '@/data/fixtures'
import type { GetSettingsQuery } from '@/graphql/generated/types'
import type { User } from '@/data/types'

interface Preferences {
  name?: string
}

/**
 * Reads the current user from settings.preferences.name. Falls back to the
 * fixture USER for missing fields so nothing goes blank during loading.
 * Derives initials from whichever name we end up with.
 */
export function useMe(): User {
  const { data } = useQuery<GetSettingsQuery>(GET_SETTINGS)

  return useMemo<User>(() => {
    let name = FALLBACK.name
    try {
      if (data?.settings?.preferences) {
        const parsed = JSON.parse(data.settings.preferences)
        const prefs: Preferences = parsed ?? {}
        if (prefs.name && prefs.name.trim()) name = prefs.name.trim()
      }
    } catch {
      // bad JSON — keep fallback
    }
    const parts = name.split(/\s+/).filter(Boolean)
    const initials = (parts[0]?.[0] ?? 'U') + (parts[1]?.[0] ?? '')
    return {
      name,
      handle: name.split(' ')[0]?.toLowerCase() ?? FALLBACK.handle,
      initials: initials.toUpperCase(),
      host: FALLBACK.host,
    }
  }, [data])
}

/** Time-of-day greeting — local hour, no backend required. */
export function useGreeting(): string {
  const h = new Date().getHours()
  if (h < 5) return 'Still up'
  if (h < 12) return 'Good morning'
  if (h < 18) return 'Good afternoon'
  return 'Good evening'
}
