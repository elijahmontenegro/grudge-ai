import type { ThreadDetail } from '@/hooks/useThreadMessages'

interface EmptyThreadProps {
  thread: ThreadDetail
  parentName?: string | null
}

export function EmptyThread({ thread, parentName }: EmptyThreadProps) {
  // Branched threads get a real explainer — inheritance is non-obvious
  // and the user needs to know what carries forward. Non-branched
  // fresh threads get nothing: the composer is right there and its
  // placeholder already says "continue the thread" — a big "No corpus
  // yet" block on top of an incoming stream is just noise.
  if (parentName) {
    return (
      <div className="empty-thread">
        <div className="empty-thread-rule">
          <span>
            branch point · <b>{parentName}</b> #{thread.branchPointPosition}
          </span>
        </div>
        <p className="empty-thread-lede">
          This thread branches from <b>{parentName}</b> at position <b>#{thread.branchPointPosition}</b>.
          Everything above that point is inherited; everything you send here diverges.
        </p>
        {thread.branchPointPosition != null && (
          <dl className="empty-thread-facts">
            <div>
              <dt>inherited corpus</dt>
              <dd>{thread.branchPointPosition} messages</dd>
            </div>
          </dl>
        )}
        <p className="empty-thread-hint">Send a message below to write the first divergent turn.</p>
      </div>
    )
  }
  return null
}
