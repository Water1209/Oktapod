// @vitest-environment jsdom
import { act, renderHook } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type { ProjectTurnResult } from '../../shared/api/client'
import { canRetrySend, useComposerSendController, type PendingSend } from './composer-send-controller'

describe('composer failed-send recovery', () => {
  it('retains input and attachment references and retries with the original identity', async () => {
    const transport = vi.fn().mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce({} as ProjectTurnResult)
    const success = vi.fn().mockResolvedValue(undefined)
    const failure = vi.fn()
    const { result } = renderHook(() => useComposerSendController(transport, success, failure))
    const send: PendingSend<unknown> = { clientMessageId: 'pending-one', key: 'stable-key', createdAt: Date.now(), input: { content: 'original', mode: 'single_agent', skill_id: 'skill-one', context_asset_ids: ['asset-one'], queue_kind: 'follow_up' }, attachments: [{ id: 'asset-one' }] }
    await act(() => result.current.submit(send))
    expect(result.current.failed[0]!.input).toEqual(send.input)
    expect(result.current.failed[0]!.attachments).toEqual(send.attachments)
    await act(() => result.current.submit(result.current.failed[0]!))
    expect(transport.mock.calls.map(([item]) => item.key)).toEqual(['stable-key', 'stable-key'])
    expect(result.current.failed).toEqual([])
    expect(success).toHaveBeenCalledOnce()
  })

  it('coalesces rapid retries and never submits an expired identity', async () => {
    let resolve!: (result: ProjectTurnResult) => void
    const transport = vi.fn(() => new Promise<ProjectTurnResult>((done) => { resolve = done }))
    const { result } = renderHook(() => useComposerSendController(transport, async () => {}, () => {}))
    const send = { clientMessageId: 'pending', key: 'key', createdAt: Date.now(), input: { content: 'hello' }, attachments: [] }
    await act(async () => {
      const first = result.current.submit(send)
      await result.current.submit(send)
      resolve({} as ProjectTurnResult)
      await first
    })
    expect(transport).toHaveBeenCalledOnce()
    const expired = { ...send, createdAt: Date.now() - 24 * 60 * 60 * 1000 }
    expect(canRetrySend(expired)).toBe(false)
    await act(() => result.current.submit(expired))
    expect(transport).toHaveBeenCalledOnce()
  })
})
