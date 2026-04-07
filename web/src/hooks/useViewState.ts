import { useEffect, useRef, useCallback } from 'react'
import { useMutation, useQuery, gql } from '@apollo/client'

const VIEW_STATE_QUERY = gql`
  query ViewState($threadId: ID!) {
    viewState(threadId: $threadId) {
      threadId
      scrollPosition
      expandedMessageIds
      inputDraft
      citationExpansionState
    }
  }
`

const SAVE_VIEW_STATE = gql`
  mutation SaveViewState($threadId: ID!, $state: ViewStateInput!) {
    saveViewState(threadId: $threadId, state: $state) {
      threadId
    }
  }
`

interface ViewState {
  scrollPosition: number
  expandedMessageIds: string[]
  inputDraft: string
  citationExpansionState: string
}

const DEBOUNCE_MS = 500

export function useViewState(threadId: string) {
  const { data } = useQuery(VIEW_STATE_QUERY, {
    variables: { threadId },
    skip: !threadId,
  })
  const [saveViewState] = useMutation(SAVE_VIEW_STATE)
  const timerRef = useRef<ReturnType<typeof setTimeout>>()

  const save = useCallback(
    (state: ViewState) => {
      if (timerRef.current) clearTimeout(timerRef.current)
      timerRef.current = setTimeout(() => {
        saveViewState({
          variables: {
            threadId,
            state: {
              scrollPosition: state.scrollPosition,
              expandedMessageIds: state.expandedMessageIds,
              inputDraft: state.inputDraft,
              citationExpansionState: state.citationExpansionState,
            },
          },
        })
      }, DEBOUNCE_MS)
    },
    [threadId, saveViewState],
  )

  useEffect(() => {
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current)
    }
  }, [])

  return {
    viewState: data?.viewState as ViewState | undefined,
    saveViewState: save,
  }
}
