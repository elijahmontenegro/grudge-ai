import type { ButtonHTMLAttributes, ReactNode } from 'react'

interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  icon: ReactNode
  /** Visible label for screen readers, falls through to title attr.
   *  Required because IconButtons have no visible text. */
  label: string
}

/**
 * A square button that's just an icon. The .sb-icon-btn pattern
 * appears across the chrome (sidebar collapse, topbar artifacts
 * toggle, ThreadConfigPopover gear, ArtifactsPanel close, etc.) —
 * one wrapper unifies the markup and ensures every site carries an
 * aria-label.
 */
export function IconButton({ icon, label, type = 'button', ...rest }: IconButtonProps) {
  return (
    <button type={type} className="sb-icon-btn" aria-label={label} title={label} {...rest}>
      {icon}
    </button>
  )
}
