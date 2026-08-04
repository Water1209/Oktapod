import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { router } from './app/router'
import './styles.css'
import { RunEventsProvider } from './features/runs/run-events-provider'
import { applyStoredPresentationPreferences } from './shared/presentation-preferences'

// Apply the saved theme before React paints so the shell never flashes the
// opposite color scheme while routes and user data are loading.
applyStoredPresentationPreferences()

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 2_000,
      retry: 1,
      refetchOnWindowFocus: true,
    },
  },
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RunEventsProvider><RouterProvider router={router} /></RunEventsProvider>
    </QueryClientProvider>
  </StrictMode>,
)
