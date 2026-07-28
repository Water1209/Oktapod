import { createContext } from 'react'
import type { RunEvent } from '../../shared/api/client'

export type ConnectionState = 'idle' | 'connecting' | 'live' | 'retrying'
export type RunEventListener = (event: RunEvent) => void

export type RunEventsContextValue = {
  subscribe: (runId: string, after: number, listener: RunEventListener, state: (value: ConnectionState) => void) => () => void
}

export const RunEventsContext = createContext<RunEventsContextValue | null>(null)
