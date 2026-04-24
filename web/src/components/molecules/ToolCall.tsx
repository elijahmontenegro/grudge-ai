import { type ReactNode, useEffect, useState } from 'react'
import { IconChevron } from '@/components/atoms/icons'
import type { ToolInvocation } from '@/data/types'

interface ToolCallProps {
  t: ToolInvocation
  /** Rendered inside the tool's expanded body. Used for interactive
   *  tool outputs (AskUserQuestion answer form, ExitPlanMode approval
   *  card) — they belong in the tool's own slot, not as floating cards
   *  elsewhere in the thread. See CLAUDE.md: "Tool outputs render in
   *  their tool's slot." */
  extras?: ReactNode
  /** Force-open the tool body. Set true when extras demand attention
   *  (pending question, plan awaiting approval) so the user doesn't
   *  have to hunt for the open-chevron. */
  forceOpen?: boolean
}

export function ToolCall({ t, extras, forceOpen }: ToolCallProps) {
  const [open, setOpen] = useState(!!forceOpen)
  // Re-sync the open state when forceOpen flips — a new pending question
  // or a freshly-emitted plan should snap the tool open without the
  // user clicking.
  useEffect(() => {
    if (forceOpen) setOpen(true)
  }, [forceOpen])
  const hasBody = !!extras || !!t.result
  return (
    <div className="toolcall" data-open={open}>
      <div className="toolcall-head" onClick={() => setOpen(!open)}>
        <span className="chev"><IconChevron size={10} /></span>
        <span className="toolcall-name">{t.name}</span>
        <span className="toolcall-arg">{t.args}</span>
        <span className="toolcall-status" data-s={t.status}>{t.status}</span>
      </div>
      {open && hasBody && (
        <div className="toolcall-body">
          {t.result && <div className="toolcall-result">{t.result}</div>}
          {extras}
        </div>
      )}
    </div>
  )
}
