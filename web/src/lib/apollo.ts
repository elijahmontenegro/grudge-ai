import { ApolloClient, InMemoryCache, HttpLink, split } from '@apollo/client'
import { GraphQLWsLink } from '@apollo/client/link/subscriptions'
import { getMainDefinition } from '@apollo/client/utilities'
import { createClient } from 'graphql-ws'

const httpLink = new HttpLink({
  uri: '/graphql',
})

const wsLink = new GraphQLWsLink(
  createClient({
    url: `${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}/graphql`,
    retryAttempts: Infinity,
    shouldRetry: () => true,
    retryWait: async (retries) => {
      // Exponential backoff: 1s initial, 30s max
      const delay = Math.min(1000 * Math.pow(2, retries), 30000)
      await new Promise((resolve) => setTimeout(resolve, delay))
    },
    on: {
      error: () => {
        // Silently handle WebSocket errors — reconnection handles recovery
      },
    },
  }),
)

const splitLink = split(
  ({ query }) => {
    const definition = getMainDefinition(query)
    return (
      definition.kind === 'OperationDefinition' &&
      definition.operation === 'subscription'
    )
  },
  wsLink,
  httpLink,
)

export const client = new ApolloClient({
  link: splitLink,
  cache: new InMemoryCache(),
})
