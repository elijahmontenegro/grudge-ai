import { useParams } from 'react-router'
import { gql } from '@apollo/client'
import { useQuery, useMutation } from '@apollo/client/react'
import { useState, useRef, useEffect } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Separator } from '@/components/ui/separator'
import { Badge } from '@/components/ui/badge'
import { MessageBubble } from '@/components/molecules/MessageBubble'
import { BranchNavigator } from '@/components/molecules/BranchNavigator'
import { AutonomousControls } from '@/components/organisms/AutonomousControls'
import { PlanMode } from '@/components/organisms/PlanMode'
import { IntrospectionPanel } from '@/components/organisms/IntrospectionPanel'
import { SubagentProgress } from '@/components/organisms/SubagentProgress'
import { ThreadSidebar } from '@/components/organisms/ThreadSidebar'
import { useAgentState } from '@/hooks/useAgentState'

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
  const [thinking, setThinking] = useState(false)
  const scrollRef = useRef<HTMLDivElement>(null)
  const { data, loading, refetch } = useQuery<any>(MESSAGES_QUERY, {
    variables: { threadId },
    skip: !threadId,
    pollInterval: thinking ? 1000 : 0,
  })
  const [sendMessage, { loading: sending }] = useMutation<any>(SEND_MESSAGE)
  const { state: agentState } = useAgentState(threadId ?? '')

  // Auto-scroll to bottom on new messages
  useEffect(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight
    }
  }, [data?.messages?.length])

  const handleSend = async () => {
    if (!input.trim() || !threadId || sending) return
    const content = input
    setInput('')
    setThinking(true)
    try {
      await sendMessage({
        variables: { threadId, content, scope: 'THREAD' },
      })
    } finally {
      setThinking(false)
      refetch()
    }
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
          {(sending || thinking) && (
            <Badge className="text-xs animate-pulse">Thinking...</Badge>
          )}
          {agentState && !sending && (
            <Badge
              variant={agentState.status === 'RUNNING' ? 'default' : 'secondary'}
              className="text-xs"
            >
              {agentState.mode !== 'NORMAL' ? agentState.mode : agentState.status}
            </Badge>
          )}
          <Button
            variant="ghost"
            size="sm"
            className="text-xs"
            onClick={() => setShowIntrospection(!showIntrospection)}
          >
            {showIntrospection ? 'Hide RRC' : 'RRC'}
          </Button>
        </header>

        <div className="flex flex-1 min-h-0">
          <div className="flex-1 flex flex-col min-w-0">
            <div ref={scrollRef} className="flex-1 overflow-y-auto p-4">
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
                {(sending || thinking) && (
                  <div className="p-3 rounded-lg bg-card border border-border max-w-[90%]">
                    <div className="flex items-center gap-2 text-sm text-muted">
                      <span className="inline-block w-2 h-2 rounded-full bg-primary animate-pulse" />
                      Thinking...
                    </div>
                  </div>
                )}
              </div>
            </div>

            {threadId && (
              <div className="p-3 space-y-2 border-t border-border">
                <div className="flex gap-2 max-w-3xl mx-auto flex-wrap">
                  <AutonomousControls threadId={threadId} />
                  <PlanMode threadId={threadId} />
                  <SubagentProgress threadId={threadId} />
                </div>
                <Separator />
                <div className="flex gap-2 max-w-3xl mx-auto w-full">
                  <Input
                    value={input}
                    onChange={(e) => setInput(e.target.value)}
                    onKeyDown={(e) => e.key === 'Enter' && !e.shiftKey && handleSend()}
                    placeholder="Send a message..."
                    className="flex-1"
                    disabled={sending || thinking}
                  />
                  <Button onClick={handleSend} disabled={sending || thinking || !input.trim()}>
                    {sending || thinking ? '...' : 'Send'}
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
