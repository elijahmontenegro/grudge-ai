import type { PaletteItem, User } from '@/domain/types'

// Fixtures kept only for surfaces the backend doesn't expose:
// - PALETTE_ITEMS: the command palette's static actions + settings entries.
//   Thread rows and search results are merged in live from LIST_THREADS /
//   SEARCH — these are the cross-cutting actions only.
// - USER: no `me` query in the schema. Once the backend exposes the current
//   user this becomes a hook.

const IS_MAC_DATA =
  typeof navigator !== 'undefined' &&
  /mac|iphone|ipad|ipod/i.test(navigator.platform || navigator.userAgent || '')

export const PALETTE_ITEMS: PaletteItem[] = [
  { kind: 'action', label: 'New thread', hint: IS_MAC_DATA ? '⌘N' : 'Ctrl+N' },
  { kind: 'action', label: 'Switch scope to all-threads', hint: IS_MAC_DATA ? '⌘⇧A' : 'Ctrl+Shift+A' },
  { kind: 'action', label: 'Enter plan mode', hint: IS_MAC_DATA ? '⌘P' : 'Ctrl+P' },
  { kind: 'action', label: 'Start autonomous run', hint: IS_MAC_DATA ? '⌘⇧↵' : 'Ctrl+Shift+↵' },
  { kind: 'setting', label: 'Configure providers', hint: 'settings' },
]

export const USER: User = {
  name: 'John Doe',
  handle: 'john',
  initials: 'JD',
  host: 'grudge.localhost:8420',
}
