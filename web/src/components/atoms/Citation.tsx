import { Badge } from '@/components/ui/badge'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

interface CitationProps {
  messageId: string
  score: number
  hopDepth: number
  crossThread: boolean
  threadId: string
}

export function Citation({ score, hopDepth, crossThread }: CitationProps) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="inline-flex gap-1">
          <Badge variant="secondary" className="text-xs font-mono">
            {score.toFixed(2)}
          </Badge>
          {hopDepth > 1 && (
            <Badge variant="outline" className="text-xs font-mono">
              d{hopDepth}
            </Badge>
          )}
          {crossThread && (
            <Badge variant="destructive" className="text-xs">
              cross
            </Badge>
          )}
        </span>
      </TooltipTrigger>
      <TooltipContent>
        <p>RRC prerequisite — effective score {score.toFixed(3)}, {hopDepth} hop(s)</p>
        {crossThread && <p>Selected from a different thread</p>}
      </TooltipContent>
    </Tooltip>
  )
}
