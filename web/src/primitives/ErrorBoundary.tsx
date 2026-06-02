import { Component } from 'react'
import type { ReactNode, ErrorInfo } from 'react'

interface Props {
  children: ReactNode
}

interface State {
  error: Error | null
}

export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error) {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // Log for devtools — backend has no error sink so this is where it stops.
    console.error('[Grudge] React error:', error, info.componentStack)
  }

  render() {
    if (this.state.error) {
      return (
        <div
          style={{
            height: '100vh',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            padding: 48,
            background: 'var(--paper)',
            color: 'var(--ink)',
            fontFamily: 'var(--sans)',
          }}
        >
          <div style={{ maxWidth: 520 }}>
            <div
              style={{
                fontFamily: 'var(--mono)',
                fontSize: 10.5,
                letterSpacing: '0.08em',
                textTransform: 'uppercase',
                color: 'var(--danger)',
                marginBottom: 12,
              }}
            >
              React error · boundary caught
            </div>
            <h1
              style={{
                fontFamily: 'var(--serif)',
                fontSize: 26,
                fontWeight: 500,
                letterSpacing: '-0.015em',
                margin: '0 0 14px',
                color: 'var(--ink)',
              }}
            >
              Something went wrong.
            </h1>
            <pre
              style={{
                fontFamily: 'var(--mono)',
                fontSize: 12,
                color: 'var(--ink-2)',
                background: 'var(--paper-2)',
                border: '1px solid var(--rule)',
                borderRadius: 3,
                padding: 12,
                whiteSpace: 'pre-wrap',
                wordBreak: 'break-word',
                margin: '0 0 18px',
                maxHeight: 200,
                overflow: 'auto',
              }}
            >
              {this.state.error.message}
            </pre>
            <div style={{ display: 'flex', gap: 10, alignItems: 'center' }}>
              <button
                onClick={() => {
                  this.setState({ error: null })
                  window.location.reload()
                }}
                style={{
                  padding: '7px 16px',
                  background: 'var(--ink)',
                  color: 'var(--paper)',
                  borderRadius: 3,
                  fontFamily: 'var(--mono)',
                  fontSize: 11,
                  letterSpacing: '0.04em',
                  textTransform: 'uppercase',
                }}
              >
                reload
              </button>
              <button
                onClick={() => this.setState({ error: null })}
                style={{
                  padding: '7px 14px',
                  border: '1px solid var(--rule)',
                  borderRadius: 3,
                  fontFamily: 'var(--mono)',
                  fontSize: 11,
                  letterSpacing: '0.04em',
                  textTransform: 'uppercase',
                  color: 'var(--ink-2)',
                }}
              >
                continue
              </button>
              <span
                style={{
                  marginLeft: 'auto',
                  fontFamily: 'var(--mono)',
                  fontSize: 10.5,
                  color: 'var(--muted)',
                }}
              >
                full trace in console
              </span>
            </div>
          </div>
        </div>
      )
    }
    return this.props.children
  }
}
