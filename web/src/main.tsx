import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'
import { ApolloProvider } from '@apollo/client/react'
import { ErrorBoundary } from '@/primitives/ErrorBoundary'
import { client } from '@/lib/apollo'
import App from '@/App'
import './app.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ErrorBoundary>
      <ApolloProvider client={client}>
        <BrowserRouter>
          <App />
        </BrowserRouter>
      </ApolloProvider>
    </ErrorBoundary>
  </StrictMode>,
)
