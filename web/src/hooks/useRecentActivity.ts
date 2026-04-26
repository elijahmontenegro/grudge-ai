import { useMemo } from 'react'
import { useQuery } from '@apollo/client/react'
import { GET_RECENT_ACTIVITY } from '@/graphql/operations'
import { adaptActivity } from '@/domain/adapters'
import type {
  GetRecentActivityQuery,
  GetRecentActivityQueryVariables,
} from '@/graphql/generated/types'
import type { ActivityEntry } from '@/domain/types'

export interface ActivityResult {
  items: ActivityEntry[]
  loading: boolean
  error: string | null
}

export function useRecentActivity(limit = 12): ActivityResult {
  const { data, loading, error } = useQuery<
    GetRecentActivityQuery,
    GetRecentActivityQueryVariables
  >(GET_RECENT_ACTIVITY, { variables: { limit } })
  const items = useMemo<ActivityEntry[]>(
    () => (data?.recentActivity ? data.recentActivity.map(adaptActivity) : []),
    [data],
  )
  return { items, loading, error: error?.message ?? null }
}
