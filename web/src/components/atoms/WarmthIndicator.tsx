import { cn } from '@/lib/utils'

interface WarmthIndicatorProps {
  warmth: number
  className?: string
}

// WarmthIndicator shows thread activity intensity. Warmth is unbounded;
// visual scaling is logarithmic so high values don't dominate.
export function WarmthIndicator({ warmth, className }: WarmthIndicatorProps) {
  // Logarithmic scaling: log(1 + warmth) mapped to 0-100% opacity
  const intensity = Math.min(Math.log(1 + warmth) / Math.log(100), 1)
  const opacity = 0.15 + intensity * 0.85

  return (
    <span
      className={cn('inline-block w-2 h-2 rounded-full', className)}
      style={{
        backgroundColor: `rgba(124, 58, 237, ${opacity})`,
      }}
      title={`Warmth: ${warmth}`}
    />
  )
}
