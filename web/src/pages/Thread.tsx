import { useParams, useNavigate } from 'react-router'
import { gql } from '@apollo/client'
import { useQuery, useMutation } from '@apollo/client/react'
import { useState, useRef, useEffect, useCallback } from 'react'
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
import { ToolCallDisplay } from '@/components/organisms/ToolCallDisplay'
import { ThreadSidebar } from '@/components/organisms/ThreadSidebar'
import { useMessageStream } from '@/hooks/useMessageStream'
import { useAgentState } from '@/hooks/useAgentState'
import { useToolExecution } from '@/hooks/useToolExecution'
import { useViewState } from '@/hooks/useViewState'
import type { Message, SelectedMessage } from '@/graphql/generated/types'

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

const EDIT_MESSAGE = gql`
  mutation EditMessage($threadId: ID!, $messagePosition: Int!, $newContent: String!) {
    editMessage(threadId: $threadId, messagePosition: $messagePosition, newContent: $newContent) {
      id
      name
    }
  }
`

const SELECTION_QUERY = gql`
  query LatestSelection($eventId: ID!) {
    selectionResult(eventId: $eventId) {
      selected {
        messageId
        effectiveScore
        hopDepth
        crossThread
        threadId
      }
    }
  }
`

type MessagesQueryData = {
  messages: Message[]
  thread: { id: string; name: string } | null
}

export function ThreadPage() {
  const { threadId } = useParams<{ threadId: string }>()
  const navigate = useNavigate()
  const [input, setInput] = useState('')
  const [scope, setScope] = useState<'THREAD' | 'ALL_THREADS'>('THREAD')
  const [showIntrospection, setShowIntrospection] = useState(false)
  const [thinking, setThinking] = useState(false)
  const [editingMsg, setEditingMsg] = useState<Message | null>(null)
  const [editContent, setEditContent] = useState('')
  const [streamText, setStreamText] = useState('')
  const scrollRef = useRef<HTMLDivElement>(null)

  const { data, loading, refetch } = useQuery<MessagesQueryData>(MESSAGES_QUERY, {
    variables: { threadId },
    skip: !threadId,
    pollInterval: thinking ? 1000 : 0,
  })
  const [sendMessage, { loading: sending }] = useMutation<{ sendMessage: Message }>(SEND_MESSAGE)
  const [editMessage] = useMutation<{ editMessage: { id: string; name: string } }>(EDIT_MESSAGE)

  type SelectionData = { selectionResult: { selected: SelectedMessage[] } | null }
  const { data: selData } = useQuery<SelectionData>(SELECTION_QUERY, {
    variables: { eventId: threadId },
    skip: !threadId,
    pollInterval: 3000,
  })

  const { event: streamEvent } = useMessageStream(threadId ?? '')
  const { state: agentState } = useAgentState(threadId ?? '')
  const { execution: toolExec } = useToolExecution(threadId ?? '')
  const { viewState, saveViewState } = useViewState(threadId ?? '')

  // Build selection map for citation display
  const selectionMap = new Map<string, SelectedMessage>(
    selData?.selectionResult?.selected?.map((s: SelectedMessage) => [s.messageId, s]) ?? []
  )

  // Streaming text accumulation
  useEffect(() => {
    if (streamEvent) {
      if (streamEvent.done) {
        setStreamText('')
        refetch()
      } else if (streamEvent.delta) {
        setStreamText(prev => prev + streamEvent.delta)
      }
    }
  }, [streamEvent, refetch])

  // Restore input draft from view state
  useEffect(() => {
    if (viewState?.inputDraft && !input) {
      setInput(viewState.inputDraft)
    }
  }, [viewState]) // eslint-disable-line react-hooks/exhaustive-deps

  // Auto-scroll
  useEffect(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight
    }
  }, [data?.messages?.length, streamText])

  const handleSend = useCallback(async () => {
    if (!input.trim() || !threadId || sending) return
    const content = input
    setInput('')
    setThinking(true)
    saveViewState({ scrollPosition: 0, expandedMessageIds: [], inputDraft: '', citationExpansionState: '{}' })
    try {
      await sendMessage({ variables: { threadId, content, scope } })
    } finally {
      setThinking(false)
      refetch()
    }
  }, [input, threadId, sending, scope, sendMessage, refetch, saveViewState])

  const handleEdit = useCallback(async (msg: Message) => {
    if (!editContent.trim() || !threadId) return
    const result = await editMessage({
      variables: { threadId, messagePosition: msg.position, newContent: editContent },
    })
    setEditingMsg(null)
    setEditContent('')
    if (result.data?.editMessage?.id) {
      navigate(`/thread/${result.data.editMessage.id}`)
    }
  }, [editContent, threadId, editMessage, navigate])

  const handleInputChange = (value: string) => {
    setInput(value)
    saveViewState({ scrollPosition: 0, expandedMessageIds: [], inputDraft: value, citationExpansionState: '{}' })
  }

  return (
    <div className="flex h-screen">
      <ThreadSidebar />

      <main className="flex-1 flex flex-col min-w-0">
        <header className="border-b border-border p-3 flex items-center gap-2">
          <h1 className="text-sm font-semibold flex-1 truncate">
            {data?.thread?.name || 'Thread'}
          </h1>
          {threadId && <BranchNavigator currentThreadId={threadId} />}
          <Badge variant="outline" className="text-[10px]">
            {data?.messages?.length ?? 0} msgs
          </Badge>
          {(sending || thinking) && (
            <Badge className="text-[10px] animate-pulse">Thinking...</Badge>
          )}
          {agentState && !sending && (
            <Badge
              variant={agentState.status === 'RUNNING' ? 'default' : 'secondary'}
              className="text-[10px]"
            >
              {agentState.mode !== 'NORMAL' ? agentState.mode : agentState.status}
            </Badge>
          )}
          <Button
            variant={showIntrospection ? 'default' : 'ghost'}
            size="sm"
            className="text-[10px] h-6"
            onClick={() => setShowIntrospection(!showIntrospection)}
          >
            RRC
          </Button>
        </header>

        <div className="flex flex-1 min-h-0">
          <div className="flex-1 flex flex-col min-w-0">
            <div ref={scrollRef} className="flex-1 overflow-y-auto p-4">
              <div className="space-y-3 max-w-3xl mx-auto">
                {loading && <p className="text-sm text-muted">Loading...</p>}
                {data?.messages?.map((msg) => (
                  editingMsg?.id === msg.id ? (
                    <div key={msg.id} className="p-3 border border-primary rounded-lg space-y-2">
                      <Input
                        value={editContent}
                        onChange={(e) => setEditContent(e.target.value)}
                        onKeyDown={(e) => e.key === 'Enter' && handleEdit(msg)}
                        autoFocus
                      />
                      <div className="flex gap-2">
                        <Button size="sm" onClick={() => handleEdit(msg)}>Branch</Button>
                        <Button size="sm" variant="ghost" onClick={() => setEditingMsg(null)}>Cancel</Button>
                      </div>
                    </div>
                  ) : (
                    <MessageBubble
                      key={msg.id}
                      message={msg}
                      selection={selectionMap.get(msg.id) ?? undefined}
                      onEdit={msg.role === 'ROLE_USER' ? () => {
                        setEditingMsg(msg)
                        setEditContent(msg.content)
                      } : undefined}
                    />
                  )
                ))}

                {toolExec && toolExec.status !== 'completed' && (
                  <ToolCallDisplay
                    callId={toolExec.callId}
                    toolName={toolExec.toolName}
                    arguments={toolExec.arguments}
                    status={toolExec.status}
                    result={toolExec.result ?? null}
                    isError={toolExec.isError ?? null}
                  />
                )}

                {(streamText || thinking) && (
                  <div className="p-3 rounded-lg bg-card border border-border max-w-[90%]">
                    {streamText ? (
                      <p className="text-sm whitespace-pre-wrap">{streamText}</p>
                    ) : (
                      <div className="flex items-center gap-2 text-sm text-muted">
                        <span className="inline-block w-2 h-2 rounded-full bg-primary animate-pulse" />
                        Thinking...
                      </div>
                    )}
                  </div>
                )}
              </div>
            </div>

            {threadId && (
              <div className="border-t border-border p-3 space-y-2">
                <div className="flex gap-2 max-w-3xl mx-auto flex-wrap">
                  <AutonomousControls threadId={threadId} />
                  <PlanMode threadId={threadId} />
                  <SubagentProgress threadId={threadId} />
                </div>
                <Separator />
                <div className="flex gap-2 max-w-3xl mx-auto w-full items-center">
                  <Button
                    variant={scope === 'ALL_THREADS' ? 'default' : 'outline'}
                    size="sm"
                    className="text-[10px] h-7 shrink-0"
                    onClick={() => setScope(s => s === 'THREAD' ? 'ALL_THREADS' : 'THREAD')}
                  >
                    {scope === 'THREAD' ? 'Thread' : 'All'}
                  </Button>
                  <Input
                    value={input}
                    onChange={(e) => handleInputChange(e.target.value)}
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
