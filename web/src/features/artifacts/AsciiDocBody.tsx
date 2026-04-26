import { useEffect, useState } from 'react'

// Asciidoctor.js is ~400KB. Only users who enter plan mode need it, so we
// dynamic-import on first render instead of bundling eagerly. Shows the
// raw AsciiDoc in a pre block while the renderer loads so nothing's hidden.

interface AsciiDocBodyProps {
  source: string
}

let processorPromise: Promise<(src: string) => string> | null = null

function loadProcessor(): Promise<(src: string) => string> {
  if (processorPromise) return processorPromise
  processorPromise = import('asciidoctor').then((mod) => {
    // CJS→ESM interop can double-wrap the default export (the `asciidoctor`
    // npm package re-exports `@asciidoctor/core` via CommonJS; Vite's
    // esbuild synthesizes a .default at each boundary). Peel through
    // nested .default until we find the factory function, or give up.
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    let candidate: any = mod
    for (let i = 0; i < 4; i++) {
      if (typeof candidate === 'function') break
      if (candidate && candidate.default) candidate = candidate.default
      else break
    }
    if (typeof candidate !== 'function') {
      throw new Error('asciidoctor module did not expose a factory function')
    }
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const processor = (candidate as () => any)()
    if (typeof processor?.convert !== 'function') {
      throw new Error('asciidoctor factory returned no .convert method')
    }
    return (src: string) => processor.convert(src, { safe: 'safe', attributes: { showtitle: true } }) as string
  })
  return processorPromise
}

export function AsciiDocBody({ source }: AsciiDocBodyProps) {
  const [html, setHtml] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    loadProcessor()
      .then((convert) => {
        if (cancelled) return
        try {
          setHtml(convert(source))
        } catch (e) {
          setError(e instanceof Error ? e.message : String(e))
        }
      })
      .catch((e) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      cancelled = true
    }
  }, [source])

  if (error) {
    return (
      <div style={{ color: 'var(--danger)', fontFamily: 'var(--mono)', fontSize: 11, margin: '6px 0' }}>
        AsciiDoc render failed: {error}
        <pre style={{ marginTop: 6, color: 'var(--ink-2)', whiteSpace: 'pre-wrap' }}>{source}</pre>
      </div>
    )
  }

  if (!html) {
    return (
      <pre
        className="adoc-pending"
        style={{ fontFamily: 'var(--mono)', fontSize: 12, whiteSpace: 'pre-wrap', color: 'var(--ink-2)' }}
      >
        {source}
      </pre>
    )
  }

  // asciidoctor.js returns sanitized HTML in `safe: 'safe'` mode; inject
  // directly. react/no-danger isn't in this project's eslint ruleset so no
  // disable comment is needed.
  return <div className="adoc-body" dangerouslySetInnerHTML={{ __html: html }} />
}
