import { useParams } from 'react-router'
import { useQuery, useMutation, gql } from '@apollo/client'
import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'
import { Badge } from '@/components/ui/badge'
import { MessageBubble } from '@/components/molecules/MessageBubble'
import { BranchNavigator } from '@/components/molecules/BranchNavigator'
import { AutonomousControls } from '@/components/organisms/AutonomousControls'
import { PlanMode } from '@/components/organisms/PlanMode'
import { IntrospectionPanel } from '@/components/organisms/IntrospectionPanel'
import { ThreadSidebar } from '@/components/organisms/ThreadSidebar'
import { useMessageStream } from '@/hooks/useMessageStream'
import { useAgentState } from '@/hooks/useAgentState'
import { useViewState } from '@/hooks/useViewState'

const MESSAGES_QUERY = gql`
  query ThreadMessages($threadId: ID!) {
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

export function ThreadPage() {
  const { threadId } = useParams<{ threadId: string }>()
  const [input, setInput] = useState('')
  const [showIntrospection, setShowIntrospection] = useState(false)
  const { data, loading, refetch } = useQuery(MESSAGES_QUERY, {
    variables: { threadId },
    skip: !threadId,
  })
  const [sendMessage, { loading: sending }] = useMutation(SEND_MESSAGE)
  const { event: streamEvent } = useMessageStream(threadId ?? '')
  const { state: agentState } = useAgentState(threadId ?? '')
  const { saveViewState } = useViewState(threadId ?? '')

  const handleSend = async () => {
    if (!input.trim() || !threadId) return
    const content = input
    setInput('')
    saveViewState({
      scrollPosition: 0,
      expandedMessageIds: [],
      inputDraft: '',
      citationExpansionState: '{}',
    })
    await sendMessage({
      variables: { threadId, content, scope: 'THREAD' },
    })
    refetch()
  }

  const handleInputChange = (value: string) => {
    setInput(value)
    saveViewState({
      scrollPosition: 0,
      expandedMessageIds: [],
      inputDraft: value,
      citationExpansionState: '{}',
    })
  }

  return (
    <div className="flex h-screen">
      <ThreadSidebar />

      <main className="flex-1 flex flex-col min-w-0">
        <header className="border-b border-border p-3 flex items-center gap-3">
          <h1 className="text-sm font-semibold flex-1 truncate">
            {data?.thread?.name || 'Thread'}
          </h1>
          {threadId && <BranchNavigator currentThreadId={threadId} />}
          <Badge variant="outline" className="text-xs">
            {data?.messages?.length ?? 0} msgs
          </Badge>
          {agentState && (
            <Badge
              variant={agentState.status === 'RUNNING' ? 'default' : 'secondary'}
              className="text-xs"
            >
              {agentState.status}
            </Badge>
          )}
          <Button
            variant="ghost"
            size="sm"
            className="text-xs"
            onClick={() => setShowIntrospection(!showIntrospection)}
          >
            {showIntrospection ? 'Hide' : 'RRC'}
          </Button>
        </header>

        <div className="flex flex-1 min-h-0">
          <div className="flex-1 flex flex-col min-w-0">
            <ScrollArea className="flex-1 p-4">
              <div className="space-y-3 max-w-3xl mx-auto">
                {loading && <p className="text-sm text-muted">Loading...</p>}
                {data?.messages?.map((msg: MessageData) => (
                  <MessageBubble
                    key={msg.id}
                    id={msg.id}
                    role={msg.role}
                    content={msg.content}
                  />
                ))}
                {streamEvent && !streamEvent.done && streamEvent.delta && (
                  <div className="p-3 rounded-lg bg-card border border-border max-w-[90%] animate-pulse">
                    <p className="text-sm whitespace-pre-wrap">{streamEvent.delta}</p>
                  </div>
                )}
              </div>
            </ScrollArea>

            {threadId && (
              <div className="p-3 space-y-2 border-t border-border">
                <div className="flex gap-2 max-w-3xl mx-auto">
                  <AutonomousControls threadId={threadId} />
                  <PlanMode threadId={threadId} />
                </div>
                <Separator />
                <div className="flex gap-2 max-w-3xl mx-auto w-full">
                  <Input
                    value={input}
                    onChange={(e) => handleInputChange(e.target.value)}
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
            )}
          </div>

          {showIntrospection && threadId && (
            <IntrospectionPanel threadId={threadId} />
          )}
        </div>
      </main>
    </div>
  )
}
