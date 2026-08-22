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

	it('does not request a semantic plan for a task that started with tools directly', async () => {
		renderStrip({ ...task, presentation_kind: 'task', status: 'running' })
		fireEvent.click(screen.getByRole('button', { name: /执行中/ }))
		expect(screen.getByText('当前任务无需预先创建计划，Agent 正在执行。')).toBeTruthy()
		await waitFor(() => expect(api.runPlan).not.toHaveBeenCalled())
	})
})
