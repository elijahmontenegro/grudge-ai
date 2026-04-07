import { gql } from '@apollo/client'
import { useSubscription } from '@apollo/client/react'
import type { ThreadStateChangesSubscription } from '@/graphql/generated/types'

const THREAD_STATE_SUBSCRIPTION = gql`
  subscription ThreadStateChanges {
    threadStateChanges {
      threadId
      status
      mode
      warmth
      name
    }
  }
`

export function useThreadStateChanges() {
  const { data, loading, error } =
    useSubscription<ThreadStateChangesSubscription>(THREAD_STATE_SUBSCRIPTION)

  return {
    event: data?.threadStateChanges ?? null,
    loading,
    error,
  }
}
