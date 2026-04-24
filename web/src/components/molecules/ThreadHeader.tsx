import { ThreadConfigPopover } from '@/components/organisms/ThreadConfigPopover'
import type { Thread } from '@/data/types'

interface ThreadHeaderProps {
  thread: Thread & { workingDirs?: string[]; sandboxed?: boolean }
  parentName?: string | null
}

export function ThreadHeader({ thread, parentName }: ThreadHeaderProps) {
  return (
    <header className="thread-header">
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 6 }}>
        <h1 style={{ margin: 0 }}>{thread.name}</h1>
        <ThreadConfigPopover
          threadId={thread.id}
          workingDirs={thread.workingDirs ?? []}
          sandboxed={thread.sandboxed ?? false}
        />
      </div>
      <div className="thread-header-meta" style={{ marginTop: 8 }}>
        <span>
          last active <b>{thread.lastActive || '—'}</b>
        </span>
        {parentName && (
          <span>
            branched from <b>{parentName}</b> at #{thread.branchAt}
          </span>
        )}
        {thread.workingDirs && thread.workingDirs.length > 0 && (
          <span>
            dirs <b>{thread.workingDirs.length}</b>
          </span>
        )}
        {thread.sandboxed && <span>sandboxed</span>}
      </div>
    </header>
  )
}
