import { IconSpinner, IconX } from '@/primitives/icons'
import { formatSize, type PendingAttachment } from '@/hooks/useAttachments'

interface AttachmentsProps {
  pending: PendingAttachment[]
  onRemove: (localId: string) => void
}

/** Pending-attachment chip strip rendered above the textarea while
 *  any uploads / done attachments are queued for the next send. */
export function Attachments({ pending, onRemove }: AttachmentsProps) {
  if (pending.length === 0) return null
  return (
    <div className="composer-attachments" role="list">
      {pending.map((p) => (
        <AttachmentChip key={p.localId} pending={p} onRemove={() => onRemove(p.localId)} />
      ))}
    </div>
  )
}

function AttachmentChip({
  pending,
  onRemove,
}: {
  pending: PendingAttachment
  onRemove: () => void
}) {
  const name = pending.meta?.filename ?? pending.file.name
  const size = pending.meta?.sizeBytes ?? pending.file.size
  const title =
    pending.status === 'error' ? pending.error || 'upload failed' : `${name} · ${formatSize(size)}`
  return (
    <span
      className="composer-attach-chip"
      data-status={pending.status}
      role="listitem"
      title={title}
    >
      {pending.status === 'uploading' && (
        <span className="composer-attach-spin" aria-hidden="true">
          <IconSpinner size={10} />
        </span>
      )}
      <span className="composer-attach-name">{name}</span>
      <span className="composer-attach-size">{formatSize(size)}</span>
      <button
        type="button"
        className="composer-attach-remove"
        onClick={onRemove}
        aria-label={`Remove ${name}`}
      >
        <IconX size={8} />
      </button>
    </span>
  )
}
