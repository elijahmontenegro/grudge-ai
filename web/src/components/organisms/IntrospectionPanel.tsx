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
  // Pass threadId as eventId — backend resolves to latest selection for the thread
  const { data: selData } = useQuery<any>(SELECTION_QUERY, {
    variables: { eventId: threadId },
    skip: !threadId,
    pollInterval: 2000,
  })
  const { data: qudData } = useQuery<any>(QUD_QUERY, {
    variables: { threadId },
  })

  return (
    <aside className="w-80 border-l border-border bg-card flex flex-col">
      <div className="p-3">
        <h3 className="text-sm font-semibold">RRC Introspection</h3>
      </div>
      <Separator />
      <ScrollArea className="flex-1">
        <div className="p-3 space-y-4">
          {selData?.selectionResult && (
            <section>
              <h4 className="text-xs font-medium text-muted mb-2">Selection</h4>
              <div className="space-y-1">
                {selData.selectionResult.selected.map((s: {
                  messageId: string; effectiveScore: number; hopDepth: number; crossThread: boolean
                }) => (
                  <div key={s.messageId} className="flex items-center gap-1 text-xs">
                    <span className="font-mono truncate flex-1">{s.messageId}</span>
                    <Badge variant="secondary" className="text-[10px]">
                      {s.effectiveScore.toFixed(2)}
                    </Badge>
                    <Badge variant="outline" className="text-[10px]">
                      d{s.hopDepth}
                    </Badge>
                    {s.crossThread && <Badge variant="destructive" className="text-[10px]">X</Badge>}
                  </div>
                ))}
              </div>

              {selData.selectionResult.excluded?.length > 0 && (
                <>
                  <h4 className="text-xs font-medium text-muted mt-3 mb-1">Excluded</h4>
                  {selData.selectionResult.excluded.map((e: {
                    messageId: string; reason: string; score: number
                  }) => (
                    <div key={e.messageId} className="text-xs text-muted flex gap-1">
                      <span className="font-mono truncate">{e.messageId}</span>
                      <span>({e.reason})</span>
                    </div>
                  ))}
                </>
              )}
            </section>
          )}

          {qudData?.qudGraph?.quds?.length > 0 && (
            <section>
              <h4 className="text-xs font-medium text-muted mb-2">QUD Graph</h4>
              <div className="space-y-2">
                {qudData.qudGraph.quds.map((q: {
                  id: string; question: string; status: string; addressedBy: string[]
                }) => (
                  <div key={q.id} className="text-xs border border-border rounded p-2">
                    <div className="flex items-center gap-1 mb-1">
                      <Badge
                        variant={q.status.includes('OPEN') ? 'default' : 'secondary'}
                        className="text-[10px]"
                      >
                        {q.status.replace('QUD_STATUS_', '')}
                      </Badge>
                    </div>
                    <p className="text-muted">{q.question}</p>
                    {q.addressedBy.length > 0 && (
                      <p className="text-[10px] text-muted mt-1">
                        Addressed by: {q.addressedBy.join(', ')}
                      </p>
                    )}
                  </div>
                ))}
              </div>
              {qudData.qudGraph.activeStack?.length > 0 && (
                <div className="mt-2">
                  <span className="text-[10px] text-muted">
                    Active: {qudData.qudGraph.activeStack.join(' → ')}
                  </span>
                </div>
              )}
            </section>
          )}
        </div>
      </ScrollArea>
    </aside>
  )
}
