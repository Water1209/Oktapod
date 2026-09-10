import { useRef, useState } from 'react'
import type { ProjectTurnInput, ProjectTurnResult } from '../../shared/api/client'

export type PendingSend<A> = {
  clientMessageId: string
  key: string
  createdAt: number
  input: ProjectTurnInput
  attachments: A[]
  error?: string
}

export function canRetrySend(send: { createdAt: number }, now = Date.now()) {
  return now - send.createdAt < 24 * 60 * 60 * 1000
}

export function createPendingSend<A>(clientMessageId: string, input: ProjectTurnInput, attachments: A[]): PendingSend<A> {
  return { clientMessageId, input, attachments, key: crypto.randomUUID(), createdAt: Date.now() }
}

export function useComposerSendController<A>(transport: (send: PendingSend<A>) => Promise<ProjectTurnResult>, success: (result: ProjectTurnResult, send: PendingSend<A>) => Promise<void>, failure: (id: string) => void) {
  const inFlight = useRef(new Set<string>())
  const [failed, setFailed] = useState<PendingSend<A>[]>([])
  const [pending, setPending] = useState(0)
  const submit = async (send: PendingSend<A>) => {
    if (inFlight.current.has(send.key) || !canRetrySend(send)) return
    inFlight.current.add(send.key)
    setPending((count) => count + 1)
    setFailed((items) => items.filter((item) => item.key !== send.key))
    try {
      const result = await transport(send)
      await success(result, send)
    } catch (error) {
      setFailed((items) => [...items.filter((item) => item.key !== send.key), { ...send, error: error instanceof Error ? error.message : '发送未完成' }])
      failure(send.clientMessageId)
    } finally {
      inFlight.current.delete(send.key)
      setPending((count) => count - 1)
    }
  }
  return { submit, failed, pending, dismiss: (key: string) => setFailed((items) => items.filter((item) => item.key !== key)) }
}
