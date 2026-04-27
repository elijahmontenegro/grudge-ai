import type { MessageStreamSubscription } from '@/graphql/generated/types'

/** Ordered timeline of what the agent emitted this turn. The agent
 *  interleaves thinking → text within a single round; the UI renders
 *  in arrival order. Tool calls don't appear here — they flow through
 *  the separate ToolExecution subscription, which carries call status
 *  transitions (pending → running → completed/failed). */
export type StreamItem =
  | { kind: 'thinking'; text: string }
  | { kind: 'text'; text: string }

export interface StreamState {
  /** Interleaved sequence in arrival order. The only thing
   *  StreamingTurn should render from; `text` / `thinking` below
   *  are derived views kept for dependents (auto-scroll length-watch
   *  effects) that don't need the timeline. */
  items: StreamItem[]
  /** id of the in-flight assistant message (from the stream) */
  messageId: string | null
  /** Flat accumulators — derived from `items`, kept so effects that
   *  watch lengths (auto-scroll) don't have to care about the
   *  timeline. */
  text: string
  thinking: string
  error: string | null
}

export const EMPTY_STREAM: StreamState = {
  items: [],
  text: '',
  thinking: '',
  messageId: null,
  error: null,
}

type StreamEvent = NonNullable<MessageStreamSubscription['messageStream']>

/**
 * Pure event-folding function. Given a current stream state and a
 * single MESSAGE_STREAM event, returns the next state.
 *
 * The interleave logic — thinking-extends-tail-thinking, text-delta
 * extends tail-text — preserves arrival order so the UI renders the
 * stream the way the model emitted it.
 */
export function applyStreamEvent(s: StreamState, ev: StreamEvent): StreamState {
  if (ev.error) return { ...s, error: ev.error }
  const items = s.items.slice()
  let { text, thinking } = s

  if (ev.thinking) {
    const last = items[items.length - 1]
    if (last && last.kind === 'thinking') {
      items[items.length - 1] = { ...last, text: last.text + ev.thinking }
    } else {
      items.push({ kind: 'thinking', text: ev.thinking })
    }
    thinking = thinking + ev.thinking
  }

  if (ev.delta) {
    const last = items[items.length - 1]
    if (last && last.kind === 'text') {
      items[items.length - 1] = { ...last, text: last.text + ev.delta }
    } else {
      items.push({ kind: 'text', text: ev.delta })
    }
    text = text + ev.delta
  }

  return { ...s, items, text, thinking, messageId: ev.messageId }
}
