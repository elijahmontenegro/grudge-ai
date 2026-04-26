import { useEffect, useState, type Dispatch, type SetStateAction } from 'react'

interface Codec<T> {
  serialize: (v: T) => string
  parse: (raw: string) => T
}

const JSON_CODEC: Codec<unknown> = {
  serialize: (v) => JSON.stringify(v),
  parse: (raw) => JSON.parse(raw),
}

/**
 * State hook that mirrors a single localStorage key. Reads once on
 * mount (lazy useState initializer); writes on every change via
 * effect. Parse / serialize default to JSON; pass a custom codec for
 * primitives stored as plain strings (e.g. an integer width).
 *
 * Failure modes — quota exceeded, private mode, parse error — drop
 * silently to the `initial` value rather than throwing. The state is
 * authoritative; localStorage is a persistence side channel.
 *
 * NOT intended for keys that change over the component's lifetime
 * (per-thread drafts in Composer). When a key changes, persist
 * effects fire with stale state under the new key. Composer uses
 * an imperative load/save pair instead — see its `loadDraft` /
 * `saveDraft` helpers for the rationale.
 */
export function useLocalStorage<T>(
  key: string,
  initial: T,
  codec?: Partial<Codec<T>>,
): [T, Dispatch<SetStateAction<T>>] {
  const serialize = codec?.serialize ?? (JSON_CODEC.serialize as Codec<T>['serialize'])
  const parse = codec?.parse ?? (JSON_CODEC.parse as Codec<T>['parse'])
  const [value, setValue] = useState<T>(() => {
    try {
      const raw = localStorage.getItem(key)
      if (raw === null) return initial
      return parse(raw)
    } catch {
      return initial
    }
  })
  useEffect(() => {
    try {
      localStorage.setItem(key, serialize(value))
    } catch {
      // quota / private mode — drop silently
    }
    // The codec functions are user-supplied; we don't depend on
    // their identity. Stable serialization is the caller's
    // responsibility.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, value])
  return [value, setValue]
}
