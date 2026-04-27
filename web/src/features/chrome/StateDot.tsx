import type { ThreadState } from '@/domain/derive'

export function StateDot({ state }: { state: ThreadState }) {
  return <span className="thread-dot" data-state={state} />
}
