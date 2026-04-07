import { useParams } from 'react-router'
import { useQuery, useMutation, gql } from '@apollo/client'
import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Badge } from '@/components/ui/badge'
import { Separator } from '@/components/ui/separator'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

const MESSAGES_QUERY = gql`
  query Messages($threadId: ID!) {
    messages(threadId: $threadId) {
      id
      role
      content
      position
      createdAt
    }
    thread(id: $threadId) {
      id
      name
    }
    selectionResult(eventId: $threadId) {
      eventId
      selected {
        messageId
        effectiveScore
        hopDepth
        crossThread
      }
    }
  }
`

const SEND_MESSAGE = gql`
  mutation SendMessage($threadId: ID!, $content: String!, $scope: SelectionScope) {
    sendMessage(threadId: $threadId, content: $content, scope: $scope) {
      id
      role
      content
      position
    }
  }
`

interface MessageData {
  id: string
  role: string
  content: string
  position: number
  createdAt: string
}

interface SelectedMessageData {
  messageId: string
  effectiveScore: number
  hopDepth: number
  crossThread: boolean
}

export function ThreadPage() {
  const { threadId } = useParams<{ threadId: string }>()
  const [input, setInput] = useState('')
  const { data, loading, refetch } = useQuery(MESSAGES_QUERY, {
    variables: { threadId },
    skip: !threadId,
  })
  const [sendMessage, { loading: sending }] = useMutation(SEND_MESSAGE)

  const selectedIds = new Set(
    data?.selectionResult?.selected?.map((s: SelectedMessageData) => s.messageId) ?? []
  )
  const selectionMap = new Map(
    data?.selectionResult?.selected?.map((s: SelectedMessageData) => [s.messageId, s]) ?? []
  )

  const handleSend = async () => {
    if (!input.trim() || !threadId) return
    await sendMessage({
      variables: { threadId, content: input, scope: 'THREAD' },
    })
    setInput('')
    refetch()
  }

  return (
    <div className="flex h-screen flex-col">
      <header className="border-b border-border p-4 flex items-center gap-3">
        <h1 className="text-lg font-semibold flex-1">
          {data?.thread?.name || 'Thread'}
        </h1>
        <Badge variant="outline">{data?.messages?.length ?? 0} messages</Badge>
      </header>

      <ScrollArea className="flex-1 p-4">
        <div className="space-y-3 max-w-3xl mx-auto">
          {loading && <p className="text-sm text-muted">Loading messages...</p>}
          {data?.messages?.map((msg: MessageData) => {
            const isUser = msg.role === 'ROLE_USER'
            const isSelected = selectedIds.has(msg.id)
            const selection = selectionMap.get(msg.id) as SelectedMessageData | undefined

            return (
              <div
                key={msg.id}
                className={`p-3 rounded-lg ${
                  isUser
                    ? 'ml-auto bg-primary text-primary-foreground max-w-[80%]'
                    : 'bg-card border border-border max-w-[90%]'
                } ${isSelected ? 'ring-2 ring-primary/30' : ''}`}
              >
                <p className="whitespace-pre-wrap text-sm">{msg.content}</p>
                {isSelected && selection && (
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <div className="mt-2 flex gap-1">
                        <Badge variant="secondary" className="text-xs">
                          score: {selection.effectiveScore.toFixed(2)}
                        </Badge>
                        <Badge variant="secondary" className="text-xs">
                          depth: {selection.hopDepth}
                        </Badge>
                        {selection.crossThread && (
                          <Badge variant="destructive" className="text-xs">cross-thread</Badge>
                        )}
                      </div>
                    </TooltipTrigger>
                    <TooltipContent>
                      RRC prerequisite — selected for this response
                    </TooltipContent>
                  </Tooltip>
                )}
              </div>
            )
          })}
        </div>
      </ScrollArea>

      <Separator />
      <div className="p-4 flex gap-2 max-w-3xl mx-auto w-full">
        <Input
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && !e.shiftKey && handleSend()}
          placeholder="Send a message..."
          className="flex-1"
          disabled={sending}
        />
        <Button onClick={handleSend} disabled={sending || !input.trim()}>
          Send
        </Button>
      </div>
    </div>
  )
}
