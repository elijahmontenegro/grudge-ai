import type { ThreadState } from '@/data/types'

export function StateDot({ state }: { state: ThreadState }) {
  return <span className="thread-dot" data-state={state} />
}
