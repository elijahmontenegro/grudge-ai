export const IS_MAC =
  typeof navigator !== 'undefined' &&
  /mac|iphone|ipad|ipod/i.test(navigator.platform || navigator.userAgent || '')

export function kbdLabel(key: string): string {
  return (IS_MAC ? '⌘' : 'Ctrl+') + key.toUpperCase()
}

export function fmtScore(n: number): string {
  return n.toFixed(2).replace(/^0/, '')
}
