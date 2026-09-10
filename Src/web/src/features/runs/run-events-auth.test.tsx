// @vitest-environment jsdom
import { useContext, type ReactNode } from 'react'
import { act, renderHook } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { expect, test, vi } from 'vitest'
import { RunEventsProvider } from './run-events-provider'
import { RunEventsContext } from './run-events-context'
import { authLifecycle } from '../../shared/auth-lifecycle'

test('authentication expiry closes SSE and rejects queued durable and live callbacks', () => {
  const sources: FakeSource[] = []
  class FakeSource extends EventTarget {
    close = vi.fn()
    constructor() { super(); sources.push(this) }
  }
  vi.stubGlobal('EventSource', FakeSource)
  const client = new QueryClient()
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}><RunEventsProvider>{children}</RunEventsProvider></QueryClientProvider>
  const hook = renderHook(() => useContext(RunEventsContext), { wrapper })
  const durable = vi.fn()
  const live = vi.fn()
  act(() => { hook.result.current!.subscribe('run-a', 0, durable, vi.fn(), live) })
  expect(sources).toHaveLength(1)
  const source = sources[0]!
  act(() => authLifecycle.invalidate())
  expect(source.close).toHaveBeenCalled()
  act(() => {
    source.dispatchEvent(new MessageEvent('run_event', { data: JSON.stringify({ id: 'private-a', stream_seq: 1, event_type: 'model.completed', payload: {} }) }))
    source.dispatchEvent(new MessageEvent('run_live', { data: JSON.stringify({ run_id: 'run-a', ephemeral_seq: 1, text: 'private-a' }) }))
  })
  expect(durable).not.toHaveBeenCalled()
  expect(live).not.toHaveBeenCalled()
  expect(client.getQueryData(['run-events', 'run-a'])).toBeUndefined()
  hook.unmount()
  client.clear()
  vi.unstubAllGlobals()
})
