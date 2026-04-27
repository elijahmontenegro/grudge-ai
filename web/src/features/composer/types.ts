import type { AgentStatus } from '@/graphql/generated/types'

export type ComposerMode = 'normal' | 'plan' | 'autonomous'
export type ComposerScope = 'thread' | 'all'
export type AutonomousDuration = '1h' | '4h' | '24h'

export const DURATIONS: AutonomousDuration[] = ['1h', '4h', '24h']

export interface AgentRetryStatus {
  attempt: number
  maxAttempts: number
  error: string | null
  nextDelayMs: number
  final: boolean
}

export interface AgentRuntime {
  status: AgentStatus
  isAutonomous: boolean
  elapsed: string | null
  retry: AgentRetryStatus | null
  pending: boolean
  onPause?: () => void
  onResume?: (correction?: string) => void
  onStop?: () => void
}
