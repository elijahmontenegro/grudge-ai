import { useEffect, useRef, useState, type Dispatch, type SetStateAction } from 'react'

interface Codec<T> {
  serialize: (v: T) => string
  parse: (raw: string) => T
}

const JSON_CODEC: Codec<unknown> = {
  serialize: (v) => JSON.stringify(v),
  parse: (raw) => JSON.parse(raw),
}

interface Options {
  /** When true and value is the empty string, remove the key
   *  instead of writing an empty string. Useful for drafts so
   *  abandoned scratch text doesn't clutter localStorage. Default
   *  false. */
  removeOnEmpty?: boolean
}

function readKey<T>(key: string | null, initial: T, parse: Codec<T>['parse']): T {
  if (!key) return initial
  try {
    const raw = localStorage.getItem(key)
    if (raw === null) return initial
    return parse(raw)
  } catch {
    return initial
  }
}

/**
 * State hook that mirrors a single localStorage key. Reads on mount
 * AND on key change (lazy useState initializer + sync effect on key
 * change); writes on value change via effect. Parse / serialize
 * default to JSON; pass a custom codec for primitives stored as
 * plain strings.
 *
 * Failure modes — quota exceeded, private mode, parse error — drop
 * silently to the `initial` value rather than throwing. The state
 * is authoritative; localStorage is a persistence side channel.
 *
 * Dynamic keys (per-thread drafts in Composer) are supported. The
 * load effect runs on key change and refreshes value from the new
 * key's storage entry. The persist effect short-circuits during the
 * one-tick window where state still holds the outgoing key's
 * value — without that guard, navigating thread A → thread B would
 * clobber B's saved draft with A's outgoing text under B's key.
 *
 * Pass `null` for `key` to skip persistence — the hook still
 * provides reactive state but doesn't read or write localStorage.
 * Useful for components whose persistence target may not exist yet
 * (a Composer pre-thread-creation has nothing to key against).
 */
export function useLocalStorage<T>(
  key: string | null,
  initial: T,
  codec?: Partial<Codec<T>>,
  opts: Options = {},
): [T, Dispatch<SetStateAction<T>>] {
  const serialize = codec?.serialize ?? (JSON_CODEC.serialize as Codec<T>['serialize'])
  const parse = codec?.parse ?? (JSON_CODEC.parse as Codec<T>['parse'])
  const removeOnEmpty = opts.removeOnEmpty ?? false

  const [value, setValue] = useState<T>(() => readKey(key, initial, parse))

  // Track the key the current state was loaded under. The persist
  // effect only writes when its key matches — otherwise the
  // outgoing-state-under-incoming-key window would clobber the new
  // key's saved value before our load effect refreshes state.
  const loadedKeyRef = useRef(key)

  // On key change (after the first render), reload state from the
  // new key's storage entry.
  const firstRunRef = useRef(true)
  useEffect(() => {
    if (firstRunRef.current) {
      firstRunRef.current = false
      return
    }
    setValue(readKey(key, initial, parse))
    loadedKeyRef.current = key
    // initial / parse are user-supplied; their identity isn't part
    // of the load contract.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])

  useEffect(() => {
    // No key = no persistence. State still flows through React;
    // localStorage just doesn't see it.
    if (!key) return
    // Persist only under the key the state was actually loaded for.
    // After a key change, state still holds the outgoing thread's
    // value for one tick; the load effect above schedules a fresh
    // setValue but it hasn't applied yet. Skip writing to avoid
    // overwriting the new key's saved draft.
    if (loadedKeyRef.current !== key) return
    try {
      if (removeOnEmpty && value === '') {
        localStorage.removeItem(key)
      } else {
        localStorage.setItem(key, serialize(value))
      }
    } catch {
      // quota / private mode — drop silently
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, value])

  return [value, setValue]
}
