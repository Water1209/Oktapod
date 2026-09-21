// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, expect, it } from 'vitest'
import type { HumanAction } from '../../shared/api/client'
import { ApprovalPanel } from './approval-panel'
import { reconcileApprovals } from './approval-queue'

afterEach(cleanup)

function action(id: string, tool: string, status: HumanAction['status'] = 'waiting_user', scope = 'once') {
  return {
    id, project_id: 'project-1', conversation_id: 'conversation-1', task_id: 'task-1', run_id: 'run-1', step_id: `step-${id}`,
    plan_revision_id: 'plan-1', run_plan_item_id: 'item-1', kind: 'tool_approval', status, reviewer: 'user', risk_level: 'L2',
    title: id, reason: `reason-${id}`, request: { tool_name: tool, frozen_params: tool === 'workspace_run_command' ? { program: 'go', args: ['test'] } : { path: `${id}.md` }, approval_scope: scope },
    request_hash: `${id}-request-hash`, response: {}, requested_by: 'agent', decided_by: '', requested_at: '2026-09-15T00:00:00Z', expires_at: '2026-09-16T00:00:00Z', version: 1,
  } satisfies HumanAction
}

function renderPanel(actions: HumanAction[]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={client}><ApprovalPanel actions={actions} /></QueryClientProvider>)
}

describe('ApprovalPanel', () => {
  it('shows one approval at a time and navigates without expanding a stack', () => {
    renderPanel([action('command', 'workspace_run_command'), action('file', 'workspace_write_file')])
    expect(screen.getByText('运行工作区命令')).toBeTruthy()
    expect(screen.queryByText('写入工作区文件')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '下一项审批' }))
    expect(screen.getByText('写入工作区文件')).toBeTruthy()
    expect(screen.queryByText('运行工作区命令')).toBeNull()
  })

  it('offers batch approval only for compatible one-time tool approvals', () => {
    renderPanel([
      action('command', 'workspace_run_command'),
      action('file', 'workspace_write_file'),
      action('private', 'web_fetch', 'waiting_user', 'run_origin'),
    ])
    expect(screen.getByRole('button', { name: '全部批准一次 · 2 项' })).toBeTruthy()
    expect(screen.getByText('专项授权仍需逐项处理')).toBeTruthy()
  })

  it('keeps the reviewed order while replacing refreshed statuses and appending new approvals', () => {
    const first = action('one', 'workspace_run_command')
    const second = action('two', 'workspace_write_file')
    const refreshed = reconcileApprovals([first, second], [{ ...first, status: 'approved' }, action('three', 'workspace_write_file')])
    expect(refreshed.map((item) => `${item.id}:${item.status}`)).toEqual(['one:approved', 'two:superseded', 'three:waiting_user'])
  })
})
