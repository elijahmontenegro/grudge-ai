import ReactMarkdown from 'react-markdown'
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter'
import { oneDark } from 'react-syntax-highlighter/dist/esm/styles/prism'
import { cn } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { Message, SelectedMessage } from '@/graphql/generated/types'

interface MessageBubbleProps {
  message: Message
  selection?: SelectedMessage
  onEdit?: () => void
}

export function MessageBubble({ message, selection, onEdit }: MessageBubbleProps) {
  const isUser = message.role === 'ROLE_USER'
  const isSystem = message.role === 'ROLE_SYSTEM'

  return (
    <div
      className={cn(
        'group relative p-3 rounded-lg text-sm',
        isUser && 'ml-auto bg-primary text-primary-foreground max-w-[80%]',
        !isUser && !isSystem && 'bg-card border border-border max-w-[90%]',
        isSystem && 'bg-secondary/50 border border-border/50 max-w-[90%] italic',
        selection && 'ring-2 ring-primary/20',
      )}
    >
      <div className="prose prose-sm dark:prose-invert max-w-none">
        <ReactMarkdown
          components={{
            code({ className, children, ...props }) {
              const match = /language-(\w+)/.exec(className || '')
              const code = String(children).replace(/\n$/, '')
              return match ? (
                <SyntaxHighlighter
                  style={oneDark}
                  language={match[1]}
                  PreTag="div"
                  className="rounded text-xs"
                >
                  {code}
                </SyntaxHighlighter>
              ) : (
                <code className={cn('bg-secondary px-1 py-0.5 rounded text-xs', className)} {...props}>
                  {children}
                </code>
              )
            },
          }}
        >
          {message.content}
        </ReactMarkdown>
      </div>

      {selection && (
        <Tooltip>
          <TooltipTrigger asChild>
            <div className="mt-2 flex items-center gap-1">
              <Badge variant="secondary" className="text-[10px]">
                score {selection.effectiveScore.toFixed(3)}
              </Badge>
              {selection.hopDepth > 1 && (
                <Badge variant="outline" className="text-[10px]">
                  depth {selection.hopDepth}
                </Badge>
              )}
              {selection.crossThread && (
                <Badge variant="destructive" className="text-[10px]">cross-thread</Badge>
              )}
            </div>
          </TooltipTrigger>
          <TooltipContent>RRC prerequisite — selected for this response</TooltipContent>
        </Tooltip>
      )}

      {isUser && onEdit && (
        <button
          onClick={onEdit}
          className="absolute top-1 right-1 opacity-0 group-hover:opacity-100 text-[10px] text-muted hover:text-foreground transition-opacity"
        >
          edit
        </button>
      )}
    </div>
  )
}
