import { useCallback, useState } from 'react'
import type { AttachmentInput } from '@/graphql/generated/types'

/** Upload-response shape is AttachmentInput (the same gqlgen type the
 *  SEND_MESSAGE / START_AUTONOMOUS mutations accept). The server
 *  returns it, the client holds it, the same object is echoed back
 *  into the mutation — zero hand-rolled intermediate shape. */
export type AttachmentMeta = AttachmentInput

/** Pending-upload state for the composer. Mirrors AttachmentInput plus
 *  client-local fields for the progress UI: the original File handle
 *  (for retrying), a stable local id (used before the server
 *  responds), and a status + error string. */
export interface PendingAttachment {
  localId: string
  file: File
  status: 'uploading' | 'done' | 'error'
  error?: string
  meta?: AttachmentInput
}

/**
 * useAttachments manages a list of in-flight and completed uploads for
 * a single thread. Upload is POST /api/attachments/{threadId} with
 * multipart/form-data; the server returns an AttachmentMeta per file,
 * which we stash in `pending[i].meta`. On send, the caller pulls the
 * `.meta` slice off all done items and hands them to SEND_MESSAGE.
 *
 * Intentionally stateful across a single composer session — the
 * composer clears this on successful send so the next message starts
 * fresh. If the user abandons the draft, pending uploads stay in the
 * workspace (they're small) — a follow-up can add a GC sweep.
 */
export function useAttachments(threadId: string) {
  const [pending, setPending] = useState<PendingAttachment[]>([])

  const upload = useCallback(
    async (files: File[]) => {
      if (!threadId || files.length === 0) return
      // Optimistic placeholders so the UI can show chips immediately.
      const placeholders: PendingAttachment[] = files.map((f) => ({
        localId: crypto.randomUUID(),
        file: f,
        status: 'uploading',
      }))
      setPending((p) => [...p, ...placeholders])

      const form = new FormData()
      for (const f of files) form.append('files', f)
      try {
        const res = await fetch(`/api/attachments/${threadId}`, {
          method: 'POST',
          body: form,
        })
        if (!res.ok) {
          const msg = await res.text()
          setPending((p) =>
            p.map((x) =>
              placeholders.some((ph) => ph.localId === x.localId)
                ? { ...x, status: 'error', error: msg || `HTTP ${res.status}` }
                : x,
            ),
          )
          return
        }
        const metas = (await res.json()) as AttachmentInput[]
        // Match metas to placeholders by position — server returns
        // one per uploaded file in the same order we sent them.
        setPending((p) =>
          p.map((x) => {
            const idx = placeholders.findIndex((ph) => ph.localId === x.localId)
            if (idx < 0 || idx >= metas.length) return x
            return { ...x, status: 'done', meta: metas[idx] }
          }),
        )
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err)
        setPending((p) =>
          p.map((x) =>
            placeholders.some((ph) => ph.localId === x.localId)
              ? { ...x, status: 'error', error: msg }
              : x,
          ),
        )
      }
    },
    [threadId],
  )

  const remove = useCallback((localId: string) => {
    setPending((p) => p.filter((x) => x.localId !== localId))
  }, [])

  const clear = useCallback(() => setPending([]), [])

  /** The AttachmentInput payload for SEND_MESSAGE / START_AUTONOMOUS.
   *  Only includes successfully-uploaded items. */
  const metaForSend = useCallback((): AttachmentInput[] => {
    return pending.filter((x) => x.status === 'done' && x.meta).map((x) => x.meta!)
  }, [pending])

  return { pending, upload, remove, clear, metaForSend }
}

/** Byte-size formatter for the chip label. B, KB, MB — human-readable. */
export function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}
