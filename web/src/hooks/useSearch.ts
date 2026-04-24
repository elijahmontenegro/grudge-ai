import { useEffect, useState } from 'react'
import { useLazyQuery } from '@apollo/client/react'
import { SEARCH } from '@/graphql/operations'
import type { SearchQuery, SearchQueryVariables } from '@/graphql/generated/types'

export interface SearchHit {
  messageId: string
  threadId: string
  threadName: string
  snippet: string
}

export interface SearchResult {
  results: SearchHit[]
  loading: boolean
  error: string | null
}

export function useSearch(query: string, debounceMs = 180): SearchResult {
  const [runQuery, { data, loading, error }] = useLazyQuery<SearchQuery, SearchQueryVariables>(
    SEARCH,
  )
  const [debounced, setDebounced] = useState(query)

  useEffect(() => {
    const t = setTimeout(() => setDebounced(query), debounceMs)
    return () => clearTimeout(t)
  }, [query, debounceMs])

  useEffect(() => {
    const q = debounced.trim()
    if (q.length < 2) return
    void runQuery({ variables: { query: q, limit: 8 } })
  }, [debounced, runQuery])

  return {
    results: (data?.search as SearchHit[] | undefined) ?? [],
    loading,
    error: error?.message ?? null,
  }
}
