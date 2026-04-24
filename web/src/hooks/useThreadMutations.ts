import { useCallback } from 'react'
import { useMutation } from '@apollo/client/react'
import {
  ARCHIVE_THREAD,
  DELETE_THREAD,
  UNARCHIVE_THREAD,
  UPDATE_THREAD,
} from '@/graphql/operations'
import type {
  ArchiveThreadMutation,
  ArchiveThreadMutationVariables,
  DeleteThreadMutation,
  DeleteThreadMutationVariables,
  UnarchiveThreadMutation,
  UnarchiveThreadMutationVariables,
  UpdateThreadMutation,
  UpdateThreadMutationVariables,
} from '@/graphql/generated/types'

export interface ThreadMutations {
  archive: (id: string) => Promise<void>
  unarchive: (id: string) => Promise<void>
  rename: (id: string, name: string) => Promise<void>
  remove: (id: string) => Promise<void>
  pending: boolean
  error: string | null
}

export function useThreadMutations(): ThreadMutations {
  // Refetch by operation name so every active ListThreads observer
  // (Home uses includeArchived=false, Sidebar uses true) gets refreshed.
  // A variable-specific refetch would only hit one cache key.
  const refetch = ['ListThreads']
  const [archiveMut, archiveRes] = useMutation<
    ArchiveThreadMutation,
    ArchiveThreadMutationVariables
  >(ARCHIVE_THREAD, { refetchQueries: refetch })
  const [unarchiveMut, unarchiveRes] = useMutation<
    UnarchiveThreadMutation,
    UnarchiveThreadMutationVariables
  >(UNARCHIVE_THREAD, { refetchQueries: refetch })
  const [updateMut, updateRes] = useMutation<UpdateThreadMutation, UpdateThreadMutationVariables>(
    UPDATE_THREAD,
    { refetchQueries: refetch },
  )
  const [deleteMut, deleteRes] = useMutation<DeleteThreadMutation, DeleteThreadMutationVariables>(
    DELETE_THREAD,
    { refetchQueries: refetch },
  )

  const archive = useCallback(
    async (id: string) => {
      await archiveMut({ variables: { id } })
    },
    [archiveMut],
  )
  const unarchive = useCallback(
    async (id: string) => {
      await unarchiveMut({ variables: { id } })
    },
    [unarchiveMut],
  )
  const rename = useCallback(
    async (id: string, name: string) => {
      await updateMut({ variables: { id, name, workingDirs: null, sandboxed: null } })
    },
    [updateMut],
  )

  const remove = useCallback(
    async (id: string) => {
      await deleteMut({ variables: { id } })
    },
    [deleteMut],
  )

  return {
    archive,
    unarchive,
    rename,
    remove,
    pending:
      archiveRes.loading || unarchiveRes.loading || updateRes.loading || deleteRes.loading,
    error:
      archiveRes.error?.message ??
      unarchiveRes.error?.message ??
      updateRes.error?.message ??
      deleteRes.error?.message ??
      null,
  }
}
