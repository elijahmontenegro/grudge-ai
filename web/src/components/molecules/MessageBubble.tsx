import ReactMarkdown from 'react-markdown'
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter'
import { oneDark } from 'react-syntax-highlighter/dist/esm/styles/prism'
import { cn } from '@/lib/utils'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { Message, SelectedMessage } from '@/graphql/generated/types'

interface MessageBubbleProps {
  message: Message
  selection?: SelectedMessage
  onEdit?: () => void
}

function formatTime(dateStr: string | undefined): string {
  if (!dateStr) return ''
  const d = new Date(dateStr)
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
}

export function MessageBubble({ message, selection, onEdit }: MessageBubbleProps) {
  const isUser = message.role === 'ROLE_USER'
  const timestamp = formatTime(message.createdAt)
  const isSystem = message.role === 'ROLE_SYSTEM'

  if (isSystem) {
    return (
      <div className="animate-fade-in-up">
        <p className="text-xs text-muted-foreground/50 text-center italic py-1">
          {message.content}
        </p>
      </div>
    )
  }

  if (isUser) {
    return (
      <div className="animate-fade-in-up flex justify-end">
        <div className="group relative max-w-[70%]">
          <div className={cn(
            'user-bubble rounded-2xl rounded-br-sm px-4 py-3 text-white',
            selection && 'ring-1 ring-white/20',
          )}>
            <div className="prose prose-sm prose-invert max-w-none [&_p]:my-0.5 [&_p]:leading-relaxed">
              <ReactMarkdown>{message.content}</ReactMarkdown>
            </div>
          </div>

          {selection && <SelectionIndicator selection={selection} />}

          {timestamp && (
            <span className="absolute -left-14 bottom-0 opacity-0 group-hover:opacity-100 transition-opacity text-[10px] text-muted-foreground/30 tabular-nums whitespace-nowrap">
              {timestamp}
            </span>
          )}
          {onEdit && (
            <button
              onClick={onEdit}
              className="absolute -left-14 top-0 opacity-0 group-hover:opacity-100 transition-all text-xs text-muted-foreground/40 hover:text-muted-foreground px-2 py-1 rounded-md hover:bg-foreground/[0.04]"
            >
              Edit
            </button>
          )}
        </div>
      </div>
    )
  }

  // Assistant
  return (
    <div className="animate-fade-in-up">
      <div className="group relative max-w-[85%]">
        <div className="text-[11px] text-muted-foreground/40 font-medium mb-2 pl-0.5 flex items-center gap-2">
          <span>Spidey</span>
          {timestamp && <span className="opacity-0 group-hover:opacity-100 transition-opacity text-[10px] text-muted-foreground/20 tabular-nums">{timestamp}</span>}
        </div>
        <div className={cn(
          'pl-0.5',
          selection && 'border-l-2 border-l-primary/40 pl-4',
        )}>
          <div className="prose prose-sm dark:prose-invert max-w-none [&_p]:my-0.5 [&_p]:leading-relaxed text-[15px]">
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
                      className="!rounded-xl !text-xs !my-4 !shadow-lg"
                    >
                      {code}
                    </SyntaxHighlighter>
                  ) : (
                    <code className={cn('bg-foreground/[0.06] px-1.5 py-0.5 rounded-md text-[13px] font-mono', className)} {...props}>
                      {children}
                    </code>
                  )
                },
              }}
            >
              {message.content}
            </ReactMarkdown>
          </div>
        </div>

        {selection && <SelectionIndicator selection={selection} />}
      </div>
    </div>
  )
}

function SelectionIndicator({ selection }: { selection: SelectedMessage }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <div className="mt-2 flex items-center gap-2 text-[11px] text-muted-foreground/40 pl-0.5">
          <div className="flex items-center gap-1.5">
            <div
              className="score-bar"
              style={{ width: `${Math.max(12, selection.effectiveScore * 40)}px` }}
            />
            <span className="font-mono tabular-nums">{selection.effectiveScore.toFixed(2)}</span>
          </div>
          {selection.hopDepth > 1 && <span>d{selection.hopDepth}</span>}
          {selection.crossThread && <span className="text-primary/60">cross-thread</span>}
        </div>
      </TooltipTrigger>
      <TooltipContent side="bottom">
        RRC selected this as a prerequisite for the response
      </TooltipContent>
    </Tooltip>
  )
}
