import { useCallback } from 'react'
import { useMutation } from '@apollo/client/react'
import {
  ARCHIVE_THREAD,
  CREATE_THREAD,
  DELETE_THREAD,
  EDIT_MESSAGE,
  UNARCHIVE_THREAD,
  UPDATE_THREAD,
} from '@/graphql/operations'
import type {
  ArchiveThreadMutation,
  ArchiveThreadMutationVariables,
  CreateThreadMutation,
  CreateThreadMutationVariables,
  DeleteThreadMutation,
  DeleteThreadMutationVariables,
  EditMessageMutation,
  EditMessageMutationVariables,
  UnarchiveThreadMutation,
  UnarchiveThreadMutationVariables,
  UpdateThreadMutation,
  UpdateThreadMutationVariables,
} from '@/graphql/generated/types'

export interface ThreadMutations {
  /** Returns the new thread id, or null on failure. */
  create: (
    name?: string,
    workingDirs?: string[],
    sandboxed?: boolean,
  ) => Promise<string | null>
  /** Branches the thread at messagePosition with newContent.
   *  Returns the id of the branched thread, or null on failure. */
  editMessage: (
    threadId: string,
    position: number,
    newContent: string,
  ) => Promise<string | null>
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
  const [createMut, createRes] = useMutation<
    CreateThreadMutation,
    CreateThreadMutationVariables
  >(CREATE_THREAD, { refetchQueries: refetch })
  const [editMessageMut, editMessageRes] = useMutation<
    EditMessageMutation,
    EditMessageMutationVariables
  >(EDIT_MESSAGE, { refetchQueries: refetch })
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

  const create = useCallback(
    async (name?: string, workingDirs?: string[], sandboxed?: boolean) => {
      try {
        const res = await createMut({
          variables: {
            name: name ?? null,
            workingDirs: workingDirs ?? null,
            sandboxed: sandboxed ?? null,
          },
        })
        return res.data?.createThread.id ?? null
      } catch {
        return null
      }
    },
    [createMut],
  )
  const editMessage = useCallback(
    async (threadId: string, position: number, newContent: string) => {
      const res = await editMessageMut({
        variables: { threadId, messagePosition: position, newContent },
      })
      return res.data?.editMessage?.id ?? null
    },
    [editMessageMut],
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
    create,
    editMessage,
    archive,
    unarchive,
    rename,
    remove,
    pending:
      createRes.loading ||
      editMessageRes.loading ||
      archiveRes.loading ||
      unarchiveRes.loading ||
      updateRes.loading ||
      deleteRes.loading,
    error:
      createRes.error?.message ??
      editMessageRes.error?.message ??
      archiveRes.error?.message ??
      unarchiveRes.error?.message ??
      updateRes.error?.message ??
      deleteRes.error?.message ??
      null,
  }
}
