import { useSubscription, gql } from '@apollo/client'

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

interface ThreadStateEvent {
  threadId: string
  status: string
  mode: string
  warmth: number
  name: string
}

export function useThreadStateChanges() {
  const { data, loading, error } = useSubscription<{
    threadStateChanges: ThreadStateEvent
  }>(THREAD_STATE_SUBSCRIPTION)

  return {
    event: data?.threadStateChanges ?? null,
    loading,
    error,
  }
}
