import { useEffect, useMemo, type ReactNode } from 'react'
import type { QueryClient } from '@tanstack/react-query'
import { useQueryClient } from '@tanstack/react-query'
import type { RunEvent } from '../../shared/api/client'
import { RunEventsContext, type ConnectionState, type RunEventListener } from './run-events-context'

type StreamEntry = {
  runId: string
  cursor: number
  recent: RunEvent[]
  listeners: Set<RunEventListener>
  states: Set<(value: ConnectionState) => void>
  source?: EventSource
  retry?: number
  retryTimer?: number
  idleTimer?: number
}

class RunEventStreamManager {
  private readonly streams = new Map<string, StreamEntry>()
  private readonly onVisibility = () => {
    for (const entry of this.streams.values()) {
      if (document.visibilityState === 'hidden') this.close(entry)
      else this.connect(entry)
    }
  }

  constructor(private readonly queryClient: QueryClient) {}

  start() {
    document.addEventListener('visibilitychange', this.onVisibility)
  }

  subscribe(runId: string, after: number, listener: RunEventListener, state: (value: ConnectionState) => void) {
    let entry = this.streams.get(runId)
    if (!entry) {
      entry = { runId, cursor: after, recent: [], listeners: new Set(), states: new Set() }
      this.streams.set(runId, entry)
    }
    if (entry.idleTimer) window.clearTimeout(entry.idleTimer)
    entry.idleTimer = undefined
    entry.cursor = Math.max(entry.cursor, after)
    entry.listeners.add(listener)
    entry.states.add(state)
    // One Run has one transport cursor, but components can subscribe at
    // different times. Replay the bounded shared buffer so a late message card
    // cannot miss events already consumed by the task strip or inspector.
    entry.recent.filter((event) => event.stream_seq > after).forEach(listener)
    if (!entry.source) this.connect(entry)
    return () => {
      entry!.listeners.delete(listener)
      entry!.states.delete(state)
      if (entry!.listeners.size === 0) {
        // React effects can unsubscribe/resubscribe while a REST snapshot
        // advances. Keep the cursor buffer briefly so that lifecycle churn is
        // not interpreted as a brand-new transport.
        entry!.idleTimer = window.setTimeout(() => {
          if (entry!.listeners.size === 0) {
            this.close(entry!)
            this.streams.delete(runId)
          }
        }, 250)
      }
    }
  }

  destroy() {
    document.removeEventListener('visibilitychange', this.onVisibility)
    for (const entry of this.streams.values()) {
      this.close(entry)
      if (entry.idleTimer) window.clearTimeout(entry.idleTimer)
    }
    this.streams.clear()
  }

  private notify(entry: StreamEntry, value: ConnectionState) {
    entry.states.forEach((listener) => listener(value))
  }

  private close(entry: StreamEntry) {
    entry.source?.close()
    entry.source = undefined
    if (entry.retryTimer) window.clearTimeout(entry.retryTimer)
    entry.retryTimer = undefined
  }

  private connect(entry: StreamEntry) {
    this.close(entry)
    if (document.visibilityState === 'hidden' || entry.listeners.size === 0) return
    this.notify(entry, entry.retry ? 'retrying' : 'connecting')
    const source = new EventSource(`/api/v1/runs/${entry.runId}/events/stream?after=${entry.cursor}`)
    entry.source = source
    source.addEventListener('run_event', ((message: MessageEvent<string>) => {
      try {
        const event = JSON.parse(message.data) as RunEvent
        if (!event?.id || typeof event.stream_seq !== 'number' || event.stream_seq <= entry.cursor) return
        entry.cursor = event.stream_seq
        entry.recent.push(event)
        if (entry.recent.length > 200) entry.recent.splice(0, entry.recent.length - 200)
        this.queryClient.setQueryData<{ items: RunEvent[]; next_cursor: number }>(['run-events', entry.runId], (current) => {
          const items = current?.items ?? []
          if (items.some((item) => item.id === event.id || item.stream_seq === event.stream_seq)) {
            return { items, next_cursor: Math.max(current?.next_cursor ?? 0, event.stream_seq) }
          }
          return {
            items: [...items, event].sort((left, right) => left.stream_seq - right.stream_seq),
            next_cursor: Math.max(current?.next_cursor ?? 0, event.stream_seq),
          }
        })
        entry.retry = 0
        this.notify(entry, 'live')
        entry.listeners.forEach((listener) => listener(event))
        this.invalidate(entry.runId, event)
      } catch { /* malformed events do not advance the persisted cursor */ }
    }) as EventListener)
    source.onopen = () => {
      entry.retry = 0
      this.notify(entry, 'live')
    }
    source.onerror = () => {
      source.close()
      if (entry.source === source) entry.source = undefined
      if (document.visibilityState === 'hidden' || entry.listeners.size === 0) return
      entry.retry = Math.min((entry.retry ?? 0) + 1, 6)
      this.notify(entry, 'retrying')
      const delay = Math.min(500 * 2 ** (entry.retry - 1), 15_000)
      entry.retryTimer = window.setTimeout(() => this.connect(entry), delay)
    }
  }

  private invalidate(runId: string, event: RunEvent) {
    if (event.event_type === 'artifact.created' || event.event_type === 'file.changed') void this.queryClient.invalidateQueries({ queryKey: ['run-artifacts', runId] })
    if (event.event_type.startsWith('approval.')) void this.queryClient.invalidateQueries({ queryKey: ['approvals'] })
    if (event.event_type.startsWith('run.') || event.event_type.startsWith('step.')) void this.queryClient.invalidateQueries({ queryKey: ['tasks'] })
    if (typeof event.payload.task_id === 'string') void this.queryClient.invalidateQueries({ queryKey: ['task', event.payload.task_id] })
    if (typeof event.payload.conversation_id === 'string') void this.queryClient.invalidateQueries({ queryKey: ['conversation-messages', event.payload.conversation_id] })
    if (event.event_type.startsWith('step.') || event.event_type.startsWith('model.')) void this.queryClient.invalidateQueries({ queryKey: ['run-usage', runId] })
    if (event.event_type.startsWith('agent')) void this.queryClient.invalidateQueries({ queryKey: ['agent-team-room', runId] })
  }
}

export function RunEventsProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()
  const manager = useMemo(() => new RunEventStreamManager(queryClient), [queryClient])
  useEffect(() => {
    manager.start()
    return () => manager.destroy()
  }, [manager])
  const value = useMemo(() => ({ subscribe: manager.subscribe.bind(manager) }), [manager])
  return <RunEventsContext.Provider value={value}>{children}</RunEventsContext.Provider>
}
