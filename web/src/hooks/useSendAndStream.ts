import type { ComposerScope } from '@/features/composer/Composer'
import type { AttachmentMeta } from './useAttachments'
import { useMessageStream, type StreamState } from './useMessageStream'
import { useSendMessage } from './useSendMessage'

export type { StreamItem, StreamState } from './streamReducer'

export interface UseSendAndStream {
  send: (text: string, attachments?: AttachmentMeta[]) => Promise<void>
  streaming: boolean
  stream: StreamState
}

interface SendAndStreamOptions {
  /** External running signal — when true, the subscription stays
   *  open even if the user didn't initiate the send from this tab
   *  (autonomous run, another client, etc). */
  externalStreaming?: boolean
}

/**
 * Composite of useSendMessage + useMessageStream — the typical
 * caller (a thread page) wants the mutation, the subscription, and
 * the stream state together. The split parts are independently
 * useful: a future overlay reading another thread's stream uses
 * useMessageStream alone; a fire-and-forget send uses useSendMessage.
 */
export function useSendAndStream(
  threadId: string,
  scope: ComposerScope,
  opts: SendAndStreamOptions = {},
): UseSendAndStream {
  const { send, streaming: localStreaming } = useSendMessage(threadId, scope)
  const streaming = localStreaming || !!opts.externalStreaming
  const { stream } = useMessageStream(threadId, streaming)
  return { send, streaming, stream }
}
