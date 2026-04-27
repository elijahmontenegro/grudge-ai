import type { ReactNode } from 'react'

interface RowProps {
  /** Label text — rendered as a <label> element next to the field. */
  label: ReactNode
  children: ReactNode
}

/**
 * A label + field row, the .row pattern used in Settings and
 * FirstRun. Renders <div class="row"><label>...</label>{children}</div>.
 *
 * Form.Row keeps the markup canonical so a future visual sweep
 * (label width, gap, alignment) hits one site.
 */
function Row({ label, children }: RowProps) {
  return (
    <div className="row">
      <label>{label}</label>
      {children}
    </div>
  )
}

export const Form = { Row }
