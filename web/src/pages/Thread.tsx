import { useParams, useNavigate } from 'react-router'
import { gql } from '@apollo/client'
import { useQuery, useMutation } from '@apollo/client/react'
import { useState, useRef, useEffect, useCallback } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'
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

const RENAME_THREAD = gql`
  mutation RenameThread($id: ID!, $name: String!) {
    updateThread(id: $id, name: $name) { id name }
  }
`

type MessagesQueryData = {
  messages: Message[]
  thread: { id: string; name: string } | null
}

function autoResize(el: HTMLTextAreaElement) {
  el.style.height = 'auto'
  el.style.height = Math.min(el.scrollHeight, 200) + 'px'
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
  const [error, setError] = useState<string | null>(null)
  const [editingName, setEditingName] = useState(false)
  const [nameInput, setNameInput] = useState('')
  const scrollRef = useRef<HTMLDivElement>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  const { data, loading, refetch } = useQuery<MessagesQueryData>(MESSAGES_QUERY, {
    variables: { threadId },
    skip: !threadId,
    pollInterval: thinking ? 1000 : 0,
  })
  const [sendMessage, { loading: sending }] = useMutation<{ sendMessage: Message }>(SEND_MESSAGE)
  const [editMessage] = useMutation<{ editMessage: { id: string; name: string } }>(EDIT_MESSAGE)
  const [renameThread] = useMutation<{ updateThread: { id: string; name: string } }>(RENAME_THREAD)

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

  const selectionMap = new Map<string, SelectedMessage>(
    selData?.selectionResult?.selected?.map((s: SelectedMessage) => [s.messageId, s]) ?? []
  )

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

  useEffect(() => {
    if (viewState?.inputDraft && !input) {
      setInput(viewState.inputDraft)
    }
  }, [viewState]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight
    }
  }, [data?.messages?.length, streamText])

  // Auto-focus composer on thread navigation
  useEffect(() => {
    if (threadId && textareaRef.current) {
      textareaRef.current.focus()
    }
  }, [threadId])

  // Keyboard shortcuts
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        if (showIntrospection) setShowIntrospection(false)
        if (editingName) setEditingName(false)
        if (editingMsg) setEditingMsg(null)
      }
    }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [showIntrospection, editingName, editingMsg])

  const handleSend = useCallback(async () => {
    if (!input.trim() || !threadId || sending) return
    const content = input
    setInput('')
    if (textareaRef.current) {
      textareaRef.current.style.height = 'auto'
    }
    setThinking(true)
    saveViewState({ scrollPosition: scrollRef.current?.scrollTop ?? 0, expandedMessageIds: [], inputDraft: '', citationExpansionState: '{}' })
    try {
      setError(null)
      await sendMessage({ variables: { threadId, content, scope } })
    } catch (e) {
      const msg = e instanceof Error ? e.message : 'Failed to send message'
      setError(msg)
      setInput(content)
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

  const threadName = data?.thread?.name
  const displayName = threadName && threadName !== 'New Thread' ? threadName : 'New conversation'

  return (
    <div className="flex h-screen">
      <ThreadSidebar />

      <main className="flex-1 flex flex-col min-w-0">
        {/* Header — glass, floating */}
        <header className="glass h-12 px-5 flex items-center gap-3 shrink-0 bg-background/70 z-10">
          {editingName ? (
            <input
              value={nameInput}
              onChange={(e) => setNameInput(e.target.value)}
              onKeyDown={async (e) => {
                if (e.key === 'Enter' && nameInput.trim() && threadId) {
                  await renameThread({ variables: { id: threadId, name: nameInput.trim() } })
                  setEditingName(false)
                  refetch()
                }
                if (e.key === 'Escape') setEditingName(false)
              }}
              onBlur={() => setEditingName(false)}
              className="text-[15px] font-medium flex-1 bg-transparent border-none outline-none text-foreground/90 truncate"
              autoFocus
            />
          ) : (
            <h1
              className="text-[15px] font-medium flex-1 truncate text-foreground/90 cursor-pointer hover:text-foreground transition-colors"
              onClick={() => { setNameInput(displayName); setEditingName(true) }}
              title="Click to rename"
            >
              {displayName}
            </h1>
          )}

          <div className="flex items-center gap-2">
            {threadId && <BranchNavigator currentThreadId={threadId} />}

            {(sending || thinking) && (
              <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                <span className="inline-block w-1.5 h-1.5 rounded-full bg-primary animate-pulse-subtle" />
                <span>Generating</span>
              </div>
            )}

            {agentState && !sending && agentState.mode !== 'NORMAL' && (
              <span className="text-xs text-primary/80 font-medium">
                {agentState.mode === 'AUTONOMOUS' ? 'Autonomous' : 'Planning'}
              </span>
            )}

            <button
              onClick={() => setShowIntrospection(!showIntrospection)}
              className={cn(
                'h-7 px-2.5 rounded-lg text-xs transition-all',
                showIntrospection
                  ? 'bg-primary/15 text-primary font-medium shadow-[0_0_12px_-2px_rgba(167,139,250,0.3)]'
                  : 'text-muted-foreground hover:text-foreground hover:bg-foreground/[0.04]'
              )}
            >
              RRC
            </button>
            {/* aria-labels on icon buttons handled via title attrs */}
          </div>
        </header>

        <div className="flex flex-1 min-h-0">
          {/* Message area */}
          <div className="flex-1 flex flex-col min-w-0">
            <div ref={scrollRef} className="flex-1 overflow-y-auto">
              <div className="max-w-3xl mx-auto px-6 py-8 space-y-6">
                {loading && (
                  <p className="text-sm text-muted-foreground text-center py-12">Loading...</p>
                )}

                {!loading && data?.messages?.length === 0 && (
                  <div className="text-center py-24 space-y-3 animate-fade-in">
                    <p className="text-xl font-medium text-foreground/30">Start a conversation</p>
                    <p className="text-sm text-muted-foreground/40">
                      RRC selects only the messages that matter
                    </p>
                  </div>
                )}

                {data?.messages?.map((msg) => (
                  editingMsg?.id === msg.id ? (
                    <div key={msg.id} id={`msg-${msg.id}`} className="p-5 rounded-2xl space-y-3 bg-card shadow-lg animate-fade-in">
                      <label className="text-xs text-muted-foreground">Edit message to create a branch</label>
                      <Input
                        value={editContent}
                        onChange={(e) => setEditContent(e.target.value)}
                        onKeyDown={(e) => {
                          if (e.key === 'Enter') handleEdit(msg)
                          if (e.key === 'Escape') setEditingMsg(null)
                        }}
                        autoFocus
                      />
                      <div className="flex gap-2">
                        <Button size="sm" onClick={() => handleEdit(msg)}>Create branch</Button>
                        <Button size="sm" variant="ghost" onClick={() => setEditingMsg(null)}>Cancel</Button>
                      </div>
                    </div>
                  ) : (
                    <div key={msg.id} id={`msg-${msg.id}`}>
                      <MessageBubble
                        message={msg}
                        selection={selectionMap.get(msg.id) ?? undefined}
                        onEdit={msg.role === 'ROLE_USER' ? () => {
                          setEditingMsg(msg)
                          setEditContent(msg.content)
                        } : undefined}
                      />
                    </div>
                  )
                ))}

                {toolExec && toolExec.status !== 'completed' && (
                  <div className="animate-fade-in-up">
                    <ToolCallDisplay
                      callId={toolExec.callId}
                      toolName={toolExec.toolName}
                      arguments={toolExec.arguments}
                      status={toolExec.status}
                      result={toolExec.result ?? null}
                      isError={toolExec.isError ?? null}
                    />
                  </div>
                )}

                {/* Error display */}
                {error && (
                  <div className="animate-fade-in-up max-w-[85%]">
                    <div className="rounded-2xl px-4 py-3 bg-destructive/10 text-destructive text-sm">
                      <div className="flex items-start gap-2">
                        <span className="shrink-0 mt-0.5">&#9888;</span>
                        <div>
                          <p className="font-medium text-xs mb-1">Failed to get response</p>
                          <p className="text-xs text-destructive/70">{error}</p>
                        </div>
                        <button
                          onClick={() => setError(null)}
                          className="ml-auto shrink-0 text-destructive/40 hover:text-destructive text-xs"
                        >
                          &#10005;
                        </button>
                      </div>
                    </div>
                  </div>
                )}

                {/* Streaming / thinking */}
                {(streamText || thinking) && (
                  <div className="max-w-[85%] animate-fade-in-up">
                    <div className="text-[11px] text-muted-foreground/60 font-medium mb-2 pl-1">Spidey</div>
                    <div className="pl-1">
                      {streamText ? (
                        <p className="text-[15px] leading-relaxed whitespace-pre-wrap">{streamText}</p>
                      ) : (
                        <div className="flex items-center gap-2 text-sm text-muted-foreground/50">
                          <span className="inline-block w-1.5 h-1.5 rounded-full bg-primary animate-pulse-subtle" />
                          Thinking...
                        </div>
                      )}
                    </div>
                  </div>
                )}
              </div>
            </div>

            {/* Footer — composer */}
            {threadId && (
              <div className="pb-5 px-6">
                {/* Mode controls + active status */}
                <div className="max-w-3xl mx-auto">
                  <SubagentProgress threadId={threadId} />
                  <div className="flex items-start gap-1 mb-2">
                    <AutonomousControls threadId={threadId} />
                    <PlanMode threadId={threadId} />
                  </div>
                </div>

                {/* Composer */}
                <div className="max-w-3xl mx-auto">
                  <div className="composer">
                    <textarea
                      ref={textareaRef}
                      value={input}
                      onChange={(e) => {
                        handleInputChange(e.target.value)
                        autoResize(e.target)
                      }}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter' && !e.shiftKey) {
                          e.preventDefault()
                          handleSend()
                        }
                      }}
                      placeholder="Message Spidey..."
                      disabled={sending || thinking}
                      rows={1}
                    />
                    <div className="flex items-center justify-between px-3 pb-2.5">
                      <div className="flex items-center gap-2">
                        <button
                          onClick={() => setScope(s => s === 'THREAD' ? 'ALL_THREADS' : 'THREAD')}
                          className={cn(
                            'text-[11px] px-2 py-1 rounded-md transition-all',
                            scope === 'ALL_THREADS'
                              ? 'text-primary bg-primary/10'
                              : 'text-muted-foreground/50 hover:text-muted-foreground hover:bg-foreground/[0.04]'
                          )}
                          title={scope === 'THREAD' ? 'Searching this thread' : 'Searching all threads'}
                        >
                          {scope === 'THREAD' ? 'This thread' : 'All threads'}
                        </button>
                      </div>
                      <button
                        onClick={handleSend}
                        disabled={sending || thinking || !input.trim()}
                        aria-label="Send message"
                        className={cn(
                          'h-8 w-8 rounded-lg flex items-center justify-center transition-all',
                          input.trim()
                            ? 'bg-primary text-primary-foreground shadow-[0_0_12px_-2px_rgba(167,139,250,0.4)] hover:shadow-[0_0_16px_-2px_rgba(167,139,250,0.5)]'
                            : 'text-muted-foreground/30'
                        )}
                      >
                        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
                          <line x1="22" y1="2" x2="11" y2="13" />
                          <polygon points="22 2 15 22 11 13 2 9 22 2" />
                        </svg>
                      </button>
                    </div>
                  </div>
                </div>
              </div>
            )}
          </div>

          {/* Introspection panel */}
          {showIntrospection && threadId && (
            <div className="animate-slide-in-right">
              <IntrospectionPanel threadId={threadId} />
            </div>
          )}
        </div>
      </main>
    </div>
  )
}
