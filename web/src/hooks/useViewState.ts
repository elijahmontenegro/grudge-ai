import { useEffect, useRef, useCallback } from 'react'
import { gql } from '@apollo/client'
import { useMutation, useQuery } from '@apollo/client/react'
import type {
  ViewStateQuery,
  ViewStateQueryVariables,
  SaveViewStateMutation,
  SaveViewStateMutationVariables,
  ViewStateInput,
} from '@/graphql/generated/types'

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

const DEBOUNCE_MS = 500

export function useViewState(threadId: string) {
  const { data } = useQuery<ViewStateQuery, ViewStateQueryVariables>(
    VIEW_STATE_QUERY,
    {
      variables: { threadId },
      skip: !threadId,
    },
  )
  const [saveViewStateMutation] = useMutation<
    SaveViewStateMutation,
    SaveViewStateMutationVariables
  >(SAVE_VIEW_STATE)
  const timerRef = useRef<ReturnType<typeof setTimeout>>(undefined)

  const save = useCallback(
    (state: ViewStateInput) => {
      if (timerRef.current) clearTimeout(timerRef.current)
      timerRef.current = setTimeout(() => {
        saveViewStateMutation({
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
    [threadId, saveViewStateMutation],
  )

  useEffect(() => {
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current)
    }
  }, [])

  return {
    viewState: data?.viewState ?? undefined,
    saveViewState: save,
  }
}
