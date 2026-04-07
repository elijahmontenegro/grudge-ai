import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'
import { ApolloProvider } from '@apollo/client/react'
import { TooltipProvider } from '@/components/ui/tooltip'
import { client } from '@/lib/apollo'
import App from '@/App'
import './app.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ApolloProvider client={client}>
      <TooltipProvider>
        <BrowserRouter>
          <App />
        </BrowserRouter>
      </TooltipProvider>
    </ApolloProvider>
  </StrictMode>,
)
