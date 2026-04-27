import type { MessageStreamSubscription } from '@/graphql/generated/types'

/** Ordered timeline of what the agent emitted this turn. The agent
 *  interleaves thinking → tool_use → [tool runs] → thinking → tool_use
 *  → … within a single round, and the UI has to render them in that
 *  exact order. Collapsing all thinking into one bucket at the top
 *  destroys the story of the round. */
export type StreamItem =
  | { kind: 'thinking'; text: string }
  | { kind: 'text'; text: string }
  | { kind: 'toolCall'; id: string; name: string; arguments: string }

export interface StreamState {
  /** Interleaved sequence in arrival order. The only thing
   *  StreamingTurn should render from; `text` / `thinking` /
   *  `toolCalls` below are derived views kept for dependents that
   *  need flat accumulators. */
  items: StreamItem[]
  /** id of the in-flight assistant message (from the stream) */
  messageId: string | null
  /** Flat accumulators — derived from `items`, kept so effects that
   *  watch lengths (auto-scroll, stage-label heuristics) don't have
   *  to care about the timeline. */
  text: string
  thinking: string
  toolCalls: { id: string; name: string; arguments: string }[]
  error: string | null
}

export const EMPTY_STREAM: StreamState = {
  items: [],
  text: '',
  thinking: '',
  messageId: null,
  toolCalls: [],
  error: null,
}

type StreamEvent = NonNullable<MessageStreamSubscription['messageStream']>

/**
 * Pure event-folding function. Given a current stream state and a
 * single MESSAGE_STREAM event, returns the next state.
 *
 * The interleave logic — thinking-extends-tail-thinking, tool_use
 * closes the current thinking segment so subsequent thinking starts
 * fresh — is what produces the [think][tool][think][tool] order the
 * model emitted. Test it without the React subscription wiring.
 */
export function applyStreamEvent(s: StreamState, ev: StreamEvent): StreamState {
  if (ev.error) return { ...s, error: ev.error }
  const items = s.items.slice()
  let { text, thinking, toolCalls } = s

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

  if (ev.toolCall) {
    const id = ev.toolCall.id
    const existingIdx = items.findIndex(
      (it) => it.kind === 'toolCall' && it.id === id,
    )
    if (existingIdx >= 0) {
      const existing = items[existingIdx] as Extract<StreamItem, { kind: 'toolCall' }>
      items[existingIdx] = {
        ...existing,
        arguments: existing.arguments + (ev.toolCall.arguments || ''),
      }
    } else {
      items.push({
        kind: 'toolCall',
        id,
        name: ev.toolCall.name,
        arguments: ev.toolCall.arguments ?? '',
      })
    }
    const tcExisting = toolCalls.find((c) => c.id === id)
    if (tcExisting) {
      toolCalls = toolCalls.map((c) =>
        c.id === id
          ? { ...c, arguments: (c.arguments || '') + (ev.toolCall!.arguments || '') }
          : c,
      )
    } else {
      toolCalls = [
        ...toolCalls,
        { id, name: ev.toolCall.name, arguments: ev.toolCall.arguments ?? '' },
      ]
    }
  }

  return { ...s, items, text, thinking, toolCalls, messageId: ev.messageId }
}
