// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, type Task } from '../../shared/api/client'
import { TaskStatusStrip } from './task-status-strip'

vi.mock('../runs/use-run-events', () => ({
  useRunEvents: () => ({ data: { items: [], next_cursor: 0 }, provisionalText: '', connection: 'live' }),
}))

const task: Task = {
  id: 'task-1', project_id: 'project-1', run_id: 'run-1', conversation_id: 'conversation-1',
  title: 'Auto answer', goal: 'answer directly', mode: 'single_agent', presentation_kind: 'pending_auto',
  visibility: 'private', status: 'running', accepted: false,
  created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
}

function renderStrip(value: Task) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}><TaskStatusStrip activeTask={value} conversationId={value.conversation_id} /></QueryClientProvider>)
}

describe('TaskStatusStrip presentation kinds', () => {
  beforeEach(() => {
    vi.spyOn(api, 'runPlan').mockRejectedValue(new Error('plan must not be requested'))
    vi.spyOn(api, 'humanActions').mockResolvedValue({ items: [] })
    vi.spyOn(api, 'conversationQueue').mockResolvedValue({ items: [] })
  })

  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('shows a neutral pending state without requesting a plan', async () => {
    renderStrip(task)
    expect(screen.getByText('思考中')).toBeTruthy()
    await waitFor(() => expect(api.runPlan).not.toHaveBeenCalled())
  })

  it('hides an auto turn resolved as an ordinary answer', () => {
    const view = renderStrip({ ...task, presentation_kind: 'answer', status: 'succeeded' })
    expect(view.container.innerHTML).toBe('')
  })

  it('shows a terminal auto failure instead of leaving the composer in thinking state', () => {
    renderStrip({ ...task, status: 'failed', error_code: 'PROVIDER_REJECTED', result_summary: 'provider rejected the request' })
    expect(screen.queryByText('思考中')).toBeNull()
    expect(screen.getByRole('button', { name: /任务未能完成|执行失败/ })).toBeTruthy()
  })

	it('keeps execution records out of the plan area while the first semantic plan is pending', async () => {
		renderStrip({ ...task, presentation_kind: 'task', status: 'running' })
		fireEvent.click(screen.getByRole('button', { name: /执行中/ }))
		expect(screen.getByText(/Agent 正在生成语义执行计划/)).toBeTruthy()
		await waitFor(() => expect(api.runPlan).not.toHaveBeenCalled())
	})

  it('does not present durable execution events as a semantic plan', async () => {
    renderStrip({ ...task, presentation_kind: 'task', status: 'succeeded' })
    fireEvent.click(screen.getByRole('button', { name: /已完成/ }))
    expect(screen.getByText('执行计划')).toBeTruthy()
    expect(screen.getByText(/此历史任务未发布语义执行计划/)).toBeTruthy()
    expect(screen.queryByText(/执行记录 ·/)).toBeNull()
    await waitFor(() => expect(api.runPlan).not.toHaveBeenCalled())
  })
})
