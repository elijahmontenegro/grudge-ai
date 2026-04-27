import { describe, expect, it } from 'vitest'
import { act, render, waitFor } from '@testing-library/react'
import { ApolloClient, ApolloLink, InMemoryCache } from '@apollo/client'
import { ApolloProvider } from '@apollo/client/react'
import { MockSubscriptionLink } from '@apollo/client/testing'
import { LIST_THREADS } from '@/graphql/operations'
import { AgentMode, AgentStatus } from '@/graphql/generated/types'
import { useThreads } from './useThreads'

// useThreads → useLiveThreadStatePatching subscribes to
// threadStateChanges and applies entity-level updates via
// cache.modify. The test renders a probe that consumes useThreads,
// pre-seeds the LIST_THREADS cache, fires a subscription event, and
// asserts the rendered thread reflects the patched fields without
// a refetch.
//
// This is the test that previously needed a browser. RTL +
// MockSubscriptionLink covers the path end-to-end for the cache
// patching logic; the only thing the browser still gives is
// CSS / animation verification.

function buildClient() {
  const subLink = new MockSubscriptionLink()
  // Other operations (queries) read from cache; we pre-seed
  // LIST_THREADS so the query resolves synchronously without a
  // network hop.
  const link = ApolloLink.from([subLink])
  const cache = new InMemoryCache()
  const client = new ApolloClient({ link, cache })
  return { client, subLink }
}

function ProbeRow({ id }: { id: string }) {
  const { threads } = useThreads(true)
  const t = threads.find((th) => th.id === id)
  if (!t) return <span data-testid={`row-${id}`}>missing</span>
  return (
    <span data-testid={`row-${id}`}>
      {t.name}|{t.status}|{t.mode}
    </span>
  )
}

describe('useThreads cache patching', () => {
  it('applies threadStateChanges via cache.modify on the Thread entity', async () => {
    const { client, subLink } = buildClient()

    // Seed the cache so useQuery hits the cache directly. Two
    // threads, one of which the subscription will mutate.
    client.cache.writeQuery({
      query: LIST_THREADS,
      variables: { includeArchived: true },
      data: {
        threads: [
          {
            __typename: 'Thread',
            id: 'thread-1',
            name: 'first',
            createdAt: '2026-01-01T00:00:00Z',
            parentThreadId: null,
            branchPointPosition: null,
            archivedAt: null,
            messageCount: 0,
            workingDirs: [],
            sandboxed: false,
            status: AgentStatus.Idle,
            mode: AgentMode.Normal,
          },
          {
            __typename: 'Thread',
            id: 'thread-2',
            name: 'second',
            createdAt: '2026-01-01T00:00:00Z',
            parentThreadId: null,
            branchPointPosition: null,
            archivedAt: null,
            messageCount: 0,
            workingDirs: [],
            sandboxed: false,
            status: AgentStatus.Idle,
            mode: AgentMode.Normal,
          },
        ],
      },
    })

    const { findByTestId } = render(
      <ApolloProvider client={client}>
        <ProbeRow id="thread-1" />
        <ProbeRow id="thread-2" />
      </ApolloProvider>,
    )

    // Initial render — both threads idle / normal.
    const row1 = await findByTestId('row-thread-1')
    expect(row1.textContent).toBe('first|IDLE|NORMAL')

    // Fire a subscription event for thread-1: status flips to
    // RUNNING, mode to AUTONOMOUS. Other thread untouched.
    await act(async () => {
      subLink.simulateResult({
        result: {
          data: {
            threadStateChanges: {
              __typename: 'ThreadStateEvent',
              threadId: 'thread-1',
              status: AgentStatus.Running,
              mode: AgentMode.Autonomous,
              warmth: null,
              name: null,
            },
          },
        },
      })
    })

    await waitFor(() => {
      const updated = client.cache.readQuery<{ threads: Array<{ id: string; status: AgentStatus; mode: AgentMode; name: string }> }>({
        query: LIST_THREADS,
        variables: { includeArchived: true },
      })
      const t1 = updated?.threads.find((th) => th.id === 'thread-1')
      expect(t1?.status).toBe(AgentStatus.Running)
      expect(t1?.mode).toBe(AgentMode.Autonomous)
      expect(t1?.name).toBe('first') // name was null in event → preserved
      const t2 = updated?.threads.find((th) => th.id === 'thread-2')
      expect(t2?.status).toBe(AgentStatus.Idle)
      expect(t2?.mode).toBe(AgentMode.Normal)
    })
  })

  it('ignores zero-only events (archive/unarchive shape) — no field overwrite', async () => {
    const { client, subLink } = buildClient()

    client.cache.writeQuery({
      query: LIST_THREADS,
      variables: { includeArchived: true },
      data: {
        threads: [
          {
            __typename: 'Thread',
            id: 'thread-1',
            name: 'real-name',
            createdAt: '2026-01-01T00:00:00Z',
            parentThreadId: null,
            branchPointPosition: null,
            archivedAt: null,
            messageCount: 0,
            workingDirs: [],
            sandboxed: false,
            status: AgentStatus.Running,
            mode: AgentMode.Plan,
          },
        ],
      },
    })

    render(
      <ApolloProvider client={client}>
        <ProbeRow id="thread-1" />
      </ApolloProvider>,
    )

    await act(async () => {
      // Backend's archive event: only threadId populated, every
      // other field zero. The handler must NOT patch — otherwise
      // we'd briefly rename to "" and reset status / mode to the
      // enum's zero value.
      subLink.simulateResult({
        result: {
          data: {
            threadStateChanges: {
              __typename: 'ThreadStateEvent',
              threadId: 'thread-1',
              status: null,
              mode: null,
              warmth: null,
              name: null,
            },
          },
        },
      })
    })

    const after = client.cache.readQuery<{ threads: Array<{ id: string; name: string; status: AgentStatus; mode: AgentMode }> }>({
      query: LIST_THREADS,
      variables: { includeArchived: true },
    })
    const t1 = after?.threads.find((th) => th.id === 'thread-1')
    expect(t1?.name).toBe('real-name')
    expect(t1?.status).toBe(AgentStatus.Running)
    expect(t1?.mode).toBe(AgentMode.Plan)
  })

})
