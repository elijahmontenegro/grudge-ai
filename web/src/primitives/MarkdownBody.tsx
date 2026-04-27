import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { Components } from 'react-markdown'

// Minimal styling overrides — we want markdown to live inside the editorial
// surface, not punch its own look. Code blocks get the mono token; lists
// tighten up; links show as underlined ink. No syntax highlighting yet.
const components: Components = {
  p: ({ children }) => <p>{children}</p>,
  code: ({ className, children, ...props }) => {
    const isBlock = className?.includes('language-')
    if (isBlock) {
      return (
        <pre
          style={{
            fontFamily: 'var(--mono)',
            fontSize: 12,
            lineHeight: 1.55,
            background: 'var(--paper-3)',
            color: 'var(--ink)',
            padding: '10px 12px',
            borderRadius: 3,
            overflow: 'auto',
            margin: '10px 0',
            border: '1px solid var(--rule)',
          }}
        >
          <code className={className} {...props}>{children}</code>
        </pre>
      )
    }
    return (
      <code
        style={{
          fontFamily: 'var(--mono)',
          fontSize: 13,
          background: 'var(--paper-3)',
          padding: '1px 5px',
          borderRadius: 3,
        }}
        {...props}
      >
        {children}
      </code>
    )
  },
  ul: ({ children }) => (
    <ul style={{ paddingLeft: 20, margin: '8px 0' }}>{children}</ul>
  ),
  ol: ({ children }) => (
    <ol style={{ paddingLeft: 20, margin: '8px 0' }}>{children}</ol>
  ),
  li: ({ children }) => <li style={{ margin: '2px 0' }}>{children}</li>,
  h1: ({ children }) => (
    <h2 style={{ fontFamily: 'var(--serif)', fontSize: 22, fontWeight: 500, margin: '16px 0 10px', letterSpacing: '-0.005em' }}>
      {children}
    </h2>
  ),
  h2: ({ children }) => (
    <h3 style={{ fontFamily: 'var(--serif)', fontSize: 18, fontWeight: 500, margin: '14px 0 8px' }}>
      {children}
    </h3>
  ),
  h3: ({ children }) => (
    <h4 style={{ fontFamily: 'var(--mono)', fontSize: 12, fontWeight: 500, letterSpacing: '0.04em', textTransform: 'uppercase', color: 'var(--muted)', margin: '14px 0 6px' }}>
      {children}
    </h4>
  ),
  a: ({ children, href }) => (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      style={{ color: 'var(--ink)', textDecoration: 'underline', textUnderlineOffset: 2 }}
    >
      {children}
    </a>
  ),
  blockquote: ({ children }) => (
    <blockquote
      style={{
        borderLeft: '2px solid var(--rule)',
        paddingLeft: 12,
        color: 'var(--muted)',
        fontStyle: 'italic',
        margin: '8px 0',
      }}
    >
      {children}
    </blockquote>
  ),
  hr: () => <hr style={{ border: 0, borderTop: '1px solid var(--rule)', margin: '14px 0' }} />,
}

export function MarkdownBody({ text }: { text: string }) {
  return <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>{text}</ReactMarkdown>
}
