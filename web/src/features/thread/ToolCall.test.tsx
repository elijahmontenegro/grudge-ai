import { describe, expect, it } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { ToolCall } from './ToolCall'

// CLAUDE.md slot invariant: tool outputs render inside their tool's
// own collapsible slot. Interactive prompts (AskUserQuestion answer
// form, ExitPlanMode approval card, ToolApproval deny-reason input)
// are rendered as `extras`. Collapsing the tool collapses the
// prompt; opening the tool reveals it. There is no other place the
// prompt lives.
//
// Regression target: the prior shape rendered ToolApprovalPrompt as
// a floating card outside the thread, so the user had to scroll to
// the tool to figure out which call the prompt belonged to. The
// renderExtras plumbing collapsed those into one home.

describe('ToolCall slot invariant', () => {
  const baseProps = {
    t: {
      name: 'Bash',
      args: '"ls"',
      result: '',
      status: 'running' as const,
    },
  }
  const Extras = () => <div data-testid="extras">approve?</div>

  it('renders extras inside the toolcall-body when open', () => {
    const { container } = render(
      <ToolCall {...baseProps} extras={<Extras />} forceOpen />,
    )
    const extras = screen.getByTestId('extras')
    const body = container.querySelector('.toolcall-body')
    expect(body).not.toBeNull()
    expect(body).toContainElement(extras)
  })

  it('hides extras when collapsed — no leakage outside the tool slot', () => {
    const { container } = render(
      <ToolCall {...baseProps} extras={<Extras />} forceOpen />,
    )
    expect(screen.queryByTestId('extras')).not.toBeNull()

    // Collapse the head.
    const head = container.querySelector('.toolcall-head')!
    fireEvent.click(head)

    // CLAUDE.md test: collapse the tool. Can the user still see /
    // interact with its output somewhere? If yes, you're wrong — it
    // leaked. The extras must vanish entirely from the DOM.
    expect(screen.queryByTestId('extras')).toBeNull()
  })

  it('does not render extras outside .toolcall-body when open', () => {
    const { container } = render(
      <ToolCall {...baseProps} extras={<Extras />} forceOpen />,
    )
    const body = container.querySelector('.toolcall-body')!
    const extras = screen.getByTestId('extras')
    // Walk parents up to body, asserting the extras element is a
    // descendant of .toolcall-body (and only there).
    let cursor: Element | null = extras
    let foundBody = false
    while (cursor) {
      if (cursor === body) {
        foundBody = true
        break
      }
      cursor = cursor.parentElement
    }
    expect(foundBody).toBe(true)
  })
})
