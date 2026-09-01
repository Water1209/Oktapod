import { useContext, useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, type RunEvent } from '../../shared/api/client'
import { RunEventsContext, type ConnectionState } from './run-events-context'

export function useRunEvents(runId: string | undefined, active = true) {
  const context = useContext(RunEventsContext)
  if (!context) throw new Error('useRunEvents must be used inside RunEventsProvider')
  const initial = useQuery({
    queryKey: ['run-events', runId],
    queryFn: () => api.runEvents(runId!),
    enabled: Boolean(runId),
    staleTime: active ? 0 : 30_000,
    // SSE is the live path. A lightweight cursor snapshot is also refreshed
    // while active so a transport failure cannot leave a permanent gap.
    refetchInterval: active ? 2_000 : false,
  })
  const [live, setLive] = useState<{ runId?: string; items: RunEvent[] }>({ items: [] })
  const [connection, setConnection] = useState<{ runId?: string; value: ConnectionState }>({ value: 'idle' })
  const [provisional, setProvisional] = useState<{ runId?: string; turnId?: string; text: string; phase?: string }>({ text: '' })
  useEffect(() => {
    if (!active || !runId) return
    return context.subscribe(runId, initial.data?.next_cursor ?? 0, (event) => {
	  if (event.event_type === 'model.message' || event.event_type === 'task.succeeded' || event.event_type === 'task.failed') {
		setProvisional({ runId, text: '' })
	  }
      setLive((current) => {
        const items = current.runId === runId ? current.items : []
        return items.some((item) => item.id === event.id || item.stream_seq === event.stream_seq)
          ? current
          : { runId, items: [...items, event].sort((left, right) => left.stream_seq - right.stream_seq) }
      })
    }, (value) => setConnection({ runId, value }), (event) => {
	  setProvisional((current) => {
		const text = current.runId === runId && current.turnId === event.model_call_id ? current.text : ''
		return {
		  runId,
		  turnId: event.model_call_id,
		  text: event.kind === 'assistant_delta' ? text + (event.delta ?? '') : text,
		  phase: event.phase ?? current.phase,
		}
	  })
	})
  }, [active, context, initial.data?.next_cursor, runId])
  const items = useMemo(() => {
    const merged = new Map<number, RunEvent>()
    for (const event of initial.data?.items ?? []) merged.set(event.stream_seq, event)
    if (live.runId === runId) for (const event of live.items) merged.set(event.stream_seq, event)
    return [...merged.values()].sort((left, right) => left.stream_seq - right.stream_seq)
  }, [initial.data, live, runId])
  return {
    ...initial,
    data: { items, next_cursor: Math.max(initial.data?.next_cursor ?? 0, items.at(-1)?.stream_seq ?? 0) },
    connection: connection.runId === runId ? connection.value : 'idle',
    provisionalText: provisional.runId === runId ? provisional.text : '',
    provisionalPhase: provisional.runId === runId ? provisional.phase : undefined,
  }
}
