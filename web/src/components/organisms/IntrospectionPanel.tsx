import { gql } from '@apollo/client'
import { useQuery } from '@apollo/client/react'
import { Badge } from '@/components/ui/badge'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'

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

export function IntrospectionPanel({ threadId }: Props) {
  const { data: selData } = useQuery<any>(SELECTION_QUERY, {
    variables: { eventId: threadId },
    skip: !threadId,
    pollInterval: 2000,
  })
  const { data: msgData } = useQuery<any>(MESSAGES_QUERY, {
    variables: { threadId },
    pollInterval: 2000,
  })
  const { data: qudData } = useQuery<any>(QUD_QUERY, {
    variables: { threadId },
  })

  const messages = msgData?.messages ?? []
  const selection = selData?.selectionResult
  const selectedIds = new Set(selection?.selected?.map((s: any) => s.messageId) ?? [])
  const selectionMap = new Map(selection?.selected?.map((s: any) => [s.messageId, s]) ?? [])

  const totalMessages = messages.length
  const selectedCount = selection?.selected?.length ?? 0

  return (
    <aside className="w-96 border-l border-border bg-card flex flex-col">
      <div className="p-3">
        <h3 className="text-sm font-semibold">RRC Introspection</h3>
        {selection && (
          <p className="text-xs text-muted mt-1">
            {selectedCount} of {totalMessages} messages selected for LLM
          </p>
        )}
      </div>
      <Separator />
      <ScrollArea className="flex-1">
        <div className="p-3 space-y-4">

          {selection && (
            <section>
              <h4 className="text-xs font-semibold text-muted uppercase tracking-wider mb-2">
                What the model saw
              </h4>
              <div className="space-y-2">
                {messages.map((msg: any) => {
                  const isSelected = selectedIds.has(msg.id)
                  const sel = selectionMap.get(msg.id) as any
                  const isUser = msg.role === 'ROLE_USER'
                  const snippet = msg.content.length > 80
                    ? msg.content.slice(0, 80) + '...'
                    : msg.content

                  return (
                    <div
                      key={msg.id}
                      className={`text-xs rounded p-2 border ${
                        isSelected
                          ? 'border-primary/40 bg-primary/5'
                          : 'border-border/30 bg-secondary/30 opacity-40'
                      }`}
                    >
                      <div className="flex items-center gap-1 mb-1">
                        <Badge
                          variant={isSelected ? 'default' : 'secondary'}
                          className="text-[10px] px-1 py-0"
                        >
                          {isSelected ? 'SENT' : 'EXCLUDED'}
                        </Badge>
                        <span className="text-[10px] text-muted">
                          #{msg.position} {isUser ? 'user' : 'assistant'}
                        </span>
                        {sel && (
                          <span className="text-[10px] text-muted ml-auto">
                            score {sel.effectiveScore.toFixed(3)} · depth {sel.hopDepth}
                          </span>
                        )}
                        {sel?.crossThread && (
                          <Badge variant="destructive" className="text-[10px] px-1 py-0">
                            cross-thread
                          </Badge>
                        )}
                      </div>
                      <p className="text-muted leading-tight">{snippet}</p>
                    </div>
                  )
                })}
              </div>
            </section>
          )}

          {!selection && (
            <p className="text-xs text-muted">Send a message to see RRC selection.</p>
          )}

          {qudData?.qudGraph?.quds?.length > 0 && (
            <section>
              <Separator className="my-3" />
              <h4 className="text-xs font-semibold text-muted uppercase tracking-wider mb-2">
                QUD Graph
              </h4>
              <div className="space-y-2">
                {qudData.qudGraph.quds.map((q: any) => (
                  <div key={q.id} className="text-xs border border-border rounded p-2">
                    <div className="flex items-center gap-1 mb-1">
                      <Badge
                        variant={q.status.includes('OPEN') ? 'default' : 'secondary'}
                        className="text-[10px] px-1 py-0"
                      >
                        {q.status.replace('QUD_STATUS_', '')}
                      </Badge>
                    </div>
                    <p className="text-muted">{q.question}</p>
                  </div>
                ))}
              </div>
            </section>
          )}
        </div>
      </ScrollArea>
    </aside>
  )
}
