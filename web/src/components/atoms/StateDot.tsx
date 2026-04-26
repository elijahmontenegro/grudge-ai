import type { ThreadState } from '@/domain/types'

export function StateDot({ state }: { state: ThreadState }) {
  return <span className="thread-dot" data-state={state} />
}
