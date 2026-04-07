import { cn } from '@/lib/utils'
import { Citation } from '@/components/atoms/Citation'

interface SelectedInfo {
  messageId: string
  effectiveScore: number
  hopDepth: number
  crossThread: boolean
  threadId: string
}

interface MessageBubbleProps {
  id: string
  role: string
  content: string
  selection?: SelectedInfo
}

export function MessageBubble({ id, role, content, selection }: MessageBubbleProps) {
  const isUser = role === 'ROLE_USER'
  const isSystem = role === 'ROLE_SYSTEM'

  return (
    <div
      className={cn(
        'p-3 rounded-lg text-sm',
        isUser && 'ml-auto bg-primary text-primary-foreground max-w-[80%]',
        !isUser && !isSystem && 'bg-card border border-border max-w-[90%]',
        isSystem && 'bg-secondary/50 border border-border/50 max-w-[90%] italic',
        selection && 'ring-2 ring-primary/20',
      )}
    >
      <div className="whitespace-pre-wrap">{content}</div>
      {selection && (
        <div className="mt-2 flex items-center gap-1">
          <span className="text-xs text-muted mr-1">cited:</span>
          <Citation
            messageId={id}
            score={selection.effectiveScore}
            hopDepth={selection.hopDepth}
            crossThread={selection.crossThread}
            threadId={selection.threadId}
          />
        </div>
      )}
    </div>
  )
}
