import { gql } from '@apollo/client'
import { useQuery } from '@apollo/client/react'
import { ScrollArea } from '@/components/ui/scroll-area'
import { cn } from '@/lib/utils'

const SELECTION_QUERY = gql`
  query SelectionResult($eventId: ID!) {
    selectionResult(eventId: $eventId) {
      eventId
      scope
      threadId
      selected {
        messageId
        effectiveScore
        hopDepth
        threadId
        crossThread
        crossEncoderScore
        qudWeight
        temporalProximity
      }
      excluded {
        messageId
        reason
        score
      }
    }
  }
`

const MESSAGES_QUERY = gql`
  query IntrospectionMessages($threadId: ID!) {
    messages(threadId: $threadId) {
      id
      role
      content
      position
    }
  }
`

const QUD_QUERY = gql`
  query QUDGraph($threadId: ID!) {
    qudGraph(threadId: $threadId) {
      quds {
        id
        question
        establishedBy
        parentQudId
        status
        addressedBy
      }
      activeStack
    }
  }
`

interface Props {
  threadId: string
}

function truncate(text: string, len: number): string {
  if (text.length <= len) return text
  return text.slice(0, len).trimEnd() + '...'
}

function ScoreChip({ label, value }: { label: string; value: number }) {
  return (
    <span className="text-[9px] text-muted-foreground/40 font-mono tabular-nums">
      <span className="text-muted-foreground/25">{label}</span>
      {' '}{(value * 100).toFixed(0)}
    </span>
  )
}

export function IntrospectionPanel({ threadId }: Props) {
  const { data: selData } = useQuery<{selectionResult: {
    eventId: string; scope: string; threadId: string
    selected: { messageId: string; effectiveScore: number; hopDepth: number; threadId: string; crossThread: boolean; crossEncoderScore: number; qudWeight: number; temporalProximity: number }[]
    excluded: { messageId: string; reason: string; score: number }[]
  } | null}>(SELECTION_QUERY, {
    variables: { eventId: threadId },
    skip: !threadId,
    pollInterval: 2000,
  })
  const { data: msgData } = useQuery<{messages: { id: string; role: string; content: string; position: number }[]}>(MESSAGES_QUERY, {
    variables: { threadId },
    pollInterval: 2000,
  })
  const { data: qudData } = useQuery<{qudGraph: {
    quds: { id: string; question: string; status: string }[]
    activeStack: string[]
  } | null}>(QUD_QUERY, { variables: { threadId } })

  const messages = msgData?.messages ?? []
  const selection = selData?.selectionResult
  const selectedIds = new Set(selection?.selected?.map((s) => s.messageId) ?? [])
  const selectionMap = new Map(selection?.selected?.map((s) => [s.messageId, s]) ?? [])
  const excludedMap = new Map(selection?.excluded?.map((e) => [e.messageId, e]) ?? [])
  const selectedCount = selection?.selected?.length ?? 0
  const totalMessages = messages.length

  return (
    <aside className="w-72 flex flex-col shrink-0 glass bg-card/60">
      <div className="h-12 px-4 flex items-center shrink-0">
        <h3 className="text-xs font-medium text-muted-foreground uppercase tracking-wider">Introspection</h3>
      </div>

      <ScrollArea className="flex-1">
        <div className="px-4 pb-4 space-y-4">
          {selection ? (
            <>
              {/* Summary */}
              <div className="text-[11px] text-muted-foreground/60">
                <span className="text-primary font-semibold text-sm">{selectedCount}</span>
                <span className="mx-1">/</span>
                <span>{totalMessages}</span>
                <span className="ml-1">messages sent</span>
              </div>

              {/* Selected messages */}
              <section className="space-y-1">
                <h4 className="text-[10px] font-medium text-muted-foreground/40 uppercase tracking-widest mb-2">
                  Context
                </h4>
                {messages
                  .filter((msg) => selectedIds.has(msg.id))
                  .map((msg) => {
                    const sel = selectionMap.get(msg.id)
                    if (!sel) return null
                    const isUser = msg.role === 'ROLE_USER'
                    return (
                      <div
                        key={msg.id}
                        className="py-2 border-l-2 border-l-primary/30 pl-3 space-y-1 cursor-pointer hover:bg-foreground/[0.02] rounded-r-lg transition-colors"
                        onClick={() => {
                          const el = document.getElementById(`msg-${msg.id}`)
                          if (el) {
                            el.scrollIntoView({ behavior: 'smooth', block: 'center' })
                            el.classList.add('highlight-flash')
                            setTimeout(() => el.classList.remove('highlight-flash'), 1500)
                          }
                        }}
                        title="Click to scroll to message"
                      >
                        <div className="flex items-center justify-between">
                          <span className="text-[10px] text-muted-foreground/50">
                            {isUser ? 'You' : 'Spidey'}
                          </span>
                          <div className="flex items-center gap-1.5">
                            {sel.crossThread && (
                              <span className="text-[9px] text-primary/60 font-medium">CROSS</span>
                            )}
                            <div className="w-8 h-[3px] rounded-full bg-foreground/[0.06] overflow-hidden">
                              <div
                                className="h-full rounded-full bg-primary/60"
                                style={{ width: `${sel.effectiveScore * 100}%` }}
                              />
                            </div>
                            <span className="text-[10px] font-mono text-muted-foreground/40 tabular-nums w-6 text-right">
                              {(sel.effectiveScore * 100).toFixed(0)}
                            </span>
                          </div>
                        </div>
                        <p className="text-xs leading-relaxed text-foreground/50">
                          {truncate(msg.content, 100)}
                        </p>
                        {/* Score breakdown */}
                        {(sel.crossEncoderScore > 0 || sel.qudWeight > 0 || sel.temporalProximity > 0) && (
                          <div className="flex items-center gap-2 mt-1">
                            <ScoreChip label="CE" value={sel.crossEncoderScore} />
                            {sel.qudWeight > 0 && <ScoreChip label="QUD" value={sel.qudWeight} />}
                            <ScoreChip label="T" value={sel.temporalProximity} />
                          </div>
                        )}
                      </div>
                    )
                  })}
              </section>

              {/* Excluded */}
              {messages.filter((msg) => !selectedIds.has(msg.id)).length > 0 && (
                <section className="space-y-1 pt-2">
                  <h4 className="text-[10px] font-medium text-muted-foreground/30 uppercase tracking-widest mb-2">
                    Not sent
                  </h4>
                  {messages
                    .filter((msg) => !selectedIds.has(msg.id))
                    .map((msg) => {
                      const excl = excludedMap.get(msg.id)
                      const reason = excl?.reason?.replace('EXCLUSION_REASON_', '').toLowerCase().replace(/_/g, ' ')
                      return (
                        <div key={msg.id} className="py-1.5 pl-3">
                          <div className="flex items-center gap-1.5 mb-0.5">
                            <span className="text-[10px] text-muted-foreground/30">
                              {msg.role === 'ROLE_USER' ? 'You' : 'Spidey'}
                            </span>
                            {reason && reason !== 'unspecified' && (
                              <span className="text-[9px] text-muted-foreground/20">{reason}</span>
                            )}
                          </div>
                          <p className="text-[11px] leading-relaxed text-muted-foreground/25">
                            {truncate(msg.content, 60)}
                          </p>
                        </div>
                      )
                    })}
                </section>
              )}
            </>
          ) : (
            <p className="text-xs text-muted-foreground/30 pt-4">
              Send a message to see RRC selection
            </p>
          )}

          {/* QUD Graph */}
          {qudData?.qudGraph?.quds && qudData.qudGraph.quds.length > 0 && (
            <section className="space-y-1 pt-2">
              <h4 className="text-[10px] font-medium text-muted-foreground/30 uppercase tracking-widest mb-2">
                Questions
              </h4>
              {qudData.qudGraph.quds.map((q) => {
                const isOpen = q.status.includes('OPEN')
                return (
                  <div key={q.id} className="py-1.5 pl-3 flex gap-2">
                    <span className={cn(
                      'inline-block w-1 h-1 rounded-full mt-1.5 shrink-0',
                      isOpen ? 'bg-primary/60' : 'bg-muted-foreground/20'
                    )} />
                    <p className={cn(
                      'text-xs leading-relaxed',
                      isOpen ? 'text-foreground/50' : 'text-muted-foreground/25'
                    )}>
                      {q.question}
                    </p>
                  </div>
                )
              })}
            </section>
          )}
        </div>
      </ScrollArea>
    </aside>
  )
}
