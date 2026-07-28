// @vitest-environment jsdom
import { useEffect, useState } from 'react'
import { cleanup, render, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, type RunEvent } from '../../shared/api/client'
import { RunEventsProvider } from './run-events-provider'
import { useRunEvents } from './use-run-events'

class FakeEventSource {
  static instances: FakeEventSource[] = []
  readonly listeners = new Map<string, Set<(event: MessageEvent<string>) => void>>()
  closed = false
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null

  constructor(readonly url: string) {
    FakeEventSource.instances.push(this)
  }

  addEventListener(type: string, listener: EventListener) {
    const listeners = this.listeners.get(type) ?? new Set()
    listeners.add(listener as (event: MessageEvent<string>) => void)
    this.listeners.set(type, listeners)
  }

  close() { this.closed = true }
  emit(event: RunEvent) {
    this.listeners.get('run_event')?.forEach((listener) => listener(new MessageEvent('run_event', { data: JSON.stringify(event) })))
  }
}

function Probe({ runId, onCount }: { runId: string; onCount: (count: number) => void }) {
  const events = useRunEvents(runId, true)
  useEffect(() => { onCount(events.data.items.length) }, [events.data.items.length, onCount])
  return null
}

function LateSubscriberHarness({ onEarly, onLate }: { onEarly: (count: number) => void; onLate: (count: number) => void }) {
  const [late, setLate] = useState(false)
  return <>
    <Probe runId="run-late" onCount={onEarly} />
    {late ? <Probe runId="run-late" onCount={onLate} /> : null}
    <button type="button" onClick={() => setLate(true)}>subscribe late</button>
  </>
}

describe('RunEventsProvider', () => {
  beforeEach(() => {
    FakeEventSource.instances = []
    vi.stubGlobal('EventSource', FakeEventSource)
    vi.spyOn(api, 'runEvents').mockResolvedValue({ items: [], next_cursor: 0 })
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
  })

  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('shares one connection per run and deduplicates persisted sequence', async () => {
    const countsA: number[] = []
    const countsB: number[] = []
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(<QueryClientProvider client={client}><RunEventsProvider><Probe runId="run-1" onCount={(value) => countsA.push(value)} /><Probe runId="run-1" onCount={(value) => countsB.push(value)} /></RunEventsProvider></QueryClientProvider>)
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1))
    const event: RunEvent = { id: 'event-1', run_id: 'run-1', stream_seq: 1, event_type: 'step.started', occurred_at: new Date().toISOString(), payload: {} }
    const source = FakeEventSource.instances[0]
    if (!source) throw new Error('expected a shared EventSource')
    source.emit(event)
    source.emit(event)
    await waitFor(() => {
      expect(countsA.at(-1)).toBe(1)
      expect(countsB.at(-1)).toBe(1)
    })
  })

  it('closes while hidden and resumes from the same manager when visible', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(<QueryClientProvider client={client}><RunEventsProvider><Probe runId="run-2" onCount={() => undefined} /></RunEventsProvider></QueryClientProvider>)
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1))
    const source = FakeEventSource.instances[0]
    if (!source) throw new Error('expected an EventSource')
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
    document.dispatchEvent(new Event('visibilitychange'))
    expect(source.closed).toBe(true)
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
    document.dispatchEvent(new Event('visibilitychange'))
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(2))
  })

  it('replays events already consumed by the shared cursor to a late subscriber', async () => {
    const early: number[] = []
    const late: number[] = []
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const view = render(<QueryClientProvider client={client}><RunEventsProvider><LateSubscriberHarness onEarly={(value) => early.push(value)} onLate={(value) => late.push(value)} /></RunEventsProvider></QueryClientProvider>)
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1))
    const source = FakeEventSource.instances[0]
    if (!source) throw new Error('expected a shared EventSource')
    source.emit({ id: 'event-plan', run_id: 'run-late', stream_seq: 7, event_type: 'execution.plan_created', occurred_at: new Date().toISOString(), payload: { items: [] } })
    await waitFor(() => expect(early.at(-1)).toBe(1))
    view.getByRole('button', { name: 'subscribe late' }).click()
    await waitFor(() => expect(late.at(-1)).toBe(1))
    expect(FakeEventSource.instances).toHaveLength(1)
  })
})
