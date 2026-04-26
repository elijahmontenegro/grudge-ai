export const IS_MAC =
  typeof navigator !== 'undefined' &&
  /mac|iphone|ipad|ipod/i.test(navigator.platform || navigator.userAgent || '')

export function kbdLabel(key: string): string {
  return (IS_MAC ? '⌘' : 'Ctrl+') + key.toUpperCase()
}
