import { useEffect, useState, useSyncExternalStore } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { router } from './router'
import { RunEventsProvider } from '../features/runs/run-events-provider'
import { authLifecycle } from '../shared/auth-lifecycle'
import { LoginPage } from '../features/auth/login-page'

function Workspace() {
  const [queryClient] = useState(() => new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 2_000,
        retry: 1,
        refetchOnWindowFocus: true,
      },
    },
  }))
  useEffect(() => {
    const discard = () => { void queryClient.cancelQueries(); queryClient.clear() }
    const unsubscribe = authLifecycle.subscribe(discard)
    return () => { unsubscribe(); discard() }
  }, [queryClient])
  return <QueryClientProvider client={queryClient}><RunEventsProvider><RouterProvider router={router} /></RunEventsProvider></QueryClientProvider>
}

export function AuthenticationLifetime() {
  const invalid = useSyncExternalStore(authLifecycle.subscribe, authLifecycle.snapshot)
  if (invalid) return <SignedOut />
  return <Workspace />
}

function SignedOut() {
  const [client] = useState(() => new QueryClient())
  return <QueryClientProvider client={client}><LoginPage sessionExpired={window.sessionStorage.getItem('oktapod.authenticated') === '1'} onAuthenticated={async () => { window.location.replace('/') }} /></QueryClientProvider>
}
