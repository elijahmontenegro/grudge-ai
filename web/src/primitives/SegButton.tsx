import type { ReactNode } from 'react'

interface Option<T extends string> {
  value: T
  label: ReactNode
  disabled?: boolean
  title?: string
}

interface SegButtonProps<T extends string> {
  /** Optional leading label (e.g. "scope", "for"). Renders inline
   *  before the segmented buttons. */
  label?: ReactNode
  options: Option<T>[]
  value: T
  onChange: (next: T) => void
  /** className override on the container — useful when the segmented
   *  group needs different chrome (the Settings perm row uses
   *  border+overflow chrome that doesn't apply to scope-select). */
  className?: string
  /** title attr on the container, applied to all buttons via title bubble. */
  title?: string
}

/**
 * A segmented set of toggle buttons. The `<span class="seg">` and
 * `<span class="scope-select">` patterns scattered across Settings
 * (perm rows), Composer.Foot (mode/scope/duration), and Topbar all
 * follow the same shape: a row of buttons where one is active.
 * SegButton makes the active state declarative — `value === o.value
 * ? data-on : undefined`.
 */
export function SegButton<T extends string>({
  label,
  options,
  value,
  onChange,
  className = 'scope-select',
  title,
}: SegButtonProps<T>) {
  return (
    <span className={className} title={title}>
      {label}
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          data-on={value === o.value || undefined}
          onClick={() => onChange(o.value)}
          disabled={o.disabled}
          title={o.title}
        >
          {o.label}
        </button>
      ))}
    </span>
  )
}
