import { describe, expect, it } from 'vitest'
import { render } from '@testing-library/react'
import { IconButton } from './IconButton'
import { SegButton } from './SegButton'
import { Form } from './Form'

// These structural tests cover the "did the className passthrough
// preserve appearance" question that was the only thing genuinely
// requiring a browser. The CSS classes themselves are unchanged
// (.sb-icon-btn, .scope-select, .seg, .row); the primitives just
// wrap inline JSX that previously hardcoded them. As long as the
// emitted HTML carries the same classes / ARIA attrs / data-on
// flags, the existing CSS produces the same visual.

describe('IconButton structure', () => {
  it('emits .sb-icon-btn with aria-label and title', () => {
    const { container } = render(
      <IconButton icon={<svg data-testid="icn" />} label="thread config" />,
    )
    const btn = container.querySelector('button')
    expect(btn).not.toBeNull()
    expect(btn?.className).toBe('sb-icon-btn')
    expect(btn?.getAttribute('aria-label')).toBe('thread config')
    expect(btn?.getAttribute('title')).toBe('thread config')
    expect(btn?.getAttribute('type')).toBe('button')
  })

  it('forwards onClick and ref through', () => {
    let clicked = false
    let refTarget: HTMLButtonElement | null = null
    render(
      <IconButton
        icon={<span />}
        label="x"
        ref={(el: HTMLButtonElement | null) => {
          refTarget = el
        }}
        onClick={() => {
          clicked = true
        }}
      />,
    )
    expect(refTarget).not.toBeNull()
    refTarget!.click()
    expect(clicked).toBe(true)
  })
})

describe('SegButton structure', () => {
  it('renders one button per option with data-on flagging the active value', () => {
    const { container } = render(
      <SegButton<string>
        className="scope-select"
        value="b"
        onChange={() => {}}
        options={[
          { value: 'a', label: 'A' },
          { value: 'b', label: 'B' },
          { value: 'c', label: 'C' },
        ]}
      />,
    )
    const wrap = container.querySelector('span.scope-select')
    expect(wrap).not.toBeNull()
    const btns = wrap!.querySelectorAll('button')
    expect(btns.length).toBe(3)
    expect(btns[0].getAttribute('data-on')).toBeNull()
    expect(btns[1].getAttribute('data-on')).toBe('true')
    expect(btns[2].getAttribute('data-on')).toBeNull()
  })

  it('honors the className override (scope-select / mode-select / chats-filters / perm-select)', () => {
    for (const cls of ['scope-select', 'mode-select', 'chats-filters', 'perm-select']) {
      const { container } = render(
        <SegButton<string>
          className={cls}
          value="a"
          onChange={() => {}}
          options={[{ value: 'a', label: 'A' }]}
        />,
      )
      const wrap = container.querySelector(`span.${cls}`)
      expect(wrap, cls).not.toBeNull()
    }
  })

  it('disables the button when option.disabled is set', () => {
    const { container } = render(
      <SegButton<string>
        value="a"
        onChange={() => {}}
        options={[
          { value: 'a', label: 'A', disabled: true },
          { value: 'b', label: 'B' },
        ]}
      />,
    )
    const btns = container.querySelectorAll('button')
    expect(btns[0].disabled).toBe(true)
    expect(btns[1].disabled).toBe(false)
  })
})

describe('Form.Row structure', () => {
  it('emits .row > label + children', () => {
    const { container } = render(
      <Form.Row label="adapter">
        <input data-testid="field" defaultValue="ollama" />
      </Form.Row>,
    )
    const row = container.querySelector('div.row')
    expect(row).not.toBeNull()
    const label = row!.querySelector('label')
    expect(label?.textContent).toBe('adapter')
    const input = row!.querySelector('input[data-testid="field"]')
    expect(input).not.toBeNull()
  })

  it('preserves the label-then-children DOM order', () => {
    const { container } = render(
      <Form.Row label="model">
        <input data-testid="m" />
      </Form.Row>,
    )
    const row = container.querySelector('div.row')!
    const kids = Array.from(row.children)
    expect(kids[0].tagName).toBe('LABEL')
    expect(kids[1].tagName).toBe('INPUT')
  })
})
