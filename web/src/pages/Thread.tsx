import { useParams } from 'react-router'
import { useQuery, useMutation, gql } from '@apollo/client'
import { useState } from 'react'

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
  }
`

const SEND_MESSAGE = gql`
  mutation SendMessage($threadId: ID!, $content: String!) {
    sendMessage(threadId: $threadId, content: $content) {
      id
      role
      content
      position
    }
  }
`

export function ThreadPage() {
  const { threadId } = useParams<{ threadId: string }>()
  const [input, setInput] = useState('')
  const { data, loading, refetch } = useQuery(MESSAGES_QUERY, {
    variables: { threadId },
  })
  const [sendMessage] = useMutation(SEND_MESSAGE)

  const handleSend = async () => {
    if (!input.trim() || !threadId) return
    await sendMessage({
      variables: { threadId, content: input },
    })
    setInput('')
    refetch()
  }

  return (
    <div className="flex h-screen">
      <main className="flex-1 flex flex-col">
        <header className="border-b border-border p-4">
          <h1 className="text-lg font-semibold">
            {data?.thread?.name || 'Thread'}
          </h1>
        </header>

        <div className="flex-1 overflow-y-auto p-4 space-y-4">
          {loading && <p className="text-muted">Loading messages...</p>}
          {data?.messages?.map((msg: { id: string; role: string; content: string }) => (
            <div
              key={msg.id}
              className={`p-3 rounded-lg max-w-[80%] ${
                msg.role === 'ROLE_USER'
                  ? 'ml-auto bg-primary text-primary-foreground'
                  : 'bg-secondary'
              }`}
            >
              <p className="whitespace-pre-wrap">{msg.content}</p>
            </div>
          ))}
        </div>

        <div className="border-t border-border p-4 flex gap-2">
          <input
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && !e.shiftKey && handleSend()}
            placeholder="Send a message..."
            className="flex-1 border border-border rounded-md px-3 py-2 bg-background"
          />
          <button
            onClick={handleSend}
            className="px-4 py-2 bg-primary text-primary-foreground rounded-md"
          >
            Send
          </button>
        </div>
      </main>
    </div>
  )
}
