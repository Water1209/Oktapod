import { describe, expect, it } from 'vitest'
import type { RunEvent, Task } from '../../shared/api/client'
import { buildTaskExecutionPlan, buildTaskTimeline, taskFailureInfo } from './task-progress-model'

const task: Task = {
  id: 'task-1', project_id: 'project-1', run_id: 'run-1', conversation_id: 'conversation-1',
	title: '检索 Physical AI', goal: '检索资料', mode: 'single_agent', presentation_kind: 'task', visibility: 'private',
  status: 'failed', error_code: 'PROVIDER_UNAVAILABLE',
  result_summary: 'context deadline exceeded (Client.Timeout exceeded while awaiting headers)',
  accepted: false, created_at: '2026-07-20T11:06:01Z', updated_at: '2026-07-20T11:08:01Z',
}

describe('task progress presentation', () => {
  it('explains provider header timeout in user-facing language', () => {
    expect(taskFailureInfo(task)).toMatchObject({
      title: '模型服务请求超时',
      retryable: true,
    })
		expect(taskFailureInfo(task)?.action).toContain('自动重试 1 次')
  })

  it('turns a budget error into an actionable recovery reason', () => {
    expect(taskFailureInfo({ error_code: 'BUDGET_RESERVATION_EXCEEDED', result_summary: '' })).toMatchObject({
      title: '本次运行配额不足',
      retryable: false,
    })
    expect(taskFailureInfo({ error_code: 'BUDGET_RESERVATION_EXCEEDED', result_summary: '' })?.action).toContain('增加本次 Run 的配额')
    expect(taskFailureInfo({ error_code: 'BUDGET_EXCEEDED', result_summary: '' })).toMatchObject({
      title: '本次运行配额不足',
      retryable: false,
    })
  })

  it('does not present a failed task as successful when a legacy tool summary contradicts the terminal state', () => {
    const failure = taskFailureInfo({ error_code: 'TOOL_EXECUTION_FAILED', result_summary: '工具执行完成，并通过确定性校验。' })
    expect(failure).toMatchObject({ title: '任务已停止', retryable: false })
    expect(failure?.detail).toContain('任务整体终态为失败')
    expect(failure?.detail).not.toContain('工具执行完成')
  })

  it('offers checkpoint replanning for a scene-policy mismatch', () => {
    const failure = taskFailureInfo({ error_code: 'SCENE_POLICY_DENIED', result_summary: '' })
    expect(failure).toMatchObject({
      title: '当前场景不允许该操作',
      retryable: true,
    })
    expect(failure?.action).toContain('重新规划')
  })

  it('explains a report length completion failure', () => {
    expect(taskFailureInfo({ error_code: 'DELIVERABLE_TOO_SHORT', result_summary: '报告实际为 12000 字，未达到不少于 30000 字的验收要求。' })).toMatchObject({
      title: '报告未达到字数要求',
      retryable: true,
    })
  })

  it('explains an MCP execution deadline as recoverable', () => {
    expect(taskFailureInfo({ error_code: 'MCP_CALL_TIMEOUT', result_summary: '' })).toMatchObject({
      title: 'MCP 工具执行超时',
      retryable: true,
    })
  })

  it('explains reasoning-only truncation without claiming completion', () => {
    expect(taskFailureInfo({ error_code: 'PROVIDER_REASONING_TRUNCATED', result_summary: '' })).toMatchObject({
      title: '模型推理达到输出上限',
      retryable: true,
    })
  })

  it('explains that an explicit search budget is enforced', () => {
    expect(taskFailureInfo({ error_code: 'TOOL_BUDGET_EXCEEDED', result_summary: '' })).toMatchObject({
      title: '搜索调用已达到任务上限',
      retryable: true,
    })
  })

  it('aggregates technical events into a task and one failed execution step', () => {
    const events: RunEvent[] = [
      { id: 'event-1', run_id: 'run-1', stream_seq: 1, event_type: 'run.created', occurred_at: '2026-07-20T11:06:01Z', payload: { step_id: 'step-1' } },
      { id: 'event-2', run_id: 'run-1', stream_seq: 2, event_type: 'step.started', occurred_at: '2026-07-20T11:06:02Z', payload: { step_id: 'step-1' } },
      { id: 'event-3', run_id: 'run-1', stream_seq: 3, event_type: 'checkpoint.committed', occurred_at: '2026-07-20T11:06:03Z', payload: { step_id: 'step-1' } },
      { id: 'event-4', run_id: 'run-1', stream_seq: 4, event_type: 'step.failed', occurred_at: '2026-07-20T11:08:01Z', payload: { step_id: 'step-1', error_code: 'PROVIDER_UNAVAILABLE', result_summary: task.result_summary } },
    ]

    const timeline = buildTaskTimeline(task, events)
    expect(timeline).toHaveLength(2)
    expect(timeline[0]).toMatchObject({ label: '任务已创建', state: 'succeeded' })
    expect(timeline[1]).toMatchObject({ label: '分析目标并制定执行计划', state: 'failed', code: 'PROVIDER_UNAVAILABLE' })
  })

  it('shows the concrete tool name, input and verified result', () => {
    const events: RunEvent[] = [
      { id: 'event-1', run_id: 'run-1', stream_seq: 1, event_type: 'run.created', occurred_at: '2026-07-20T11:06:01Z', payload: {} },
      { id: 'event-2', run_id: 'run-1', stream_seq: 2, event_type: 'tool.planned', occurred_at: '2026-07-20T11:06:02Z', payload: { step_id: 'step-tool', tool_name: 'workspace_write_file' } },
      { id: 'event-3', run_id: 'run-1', stream_seq: 3, event_type: 'tool.completed', occurred_at: '2026-07-20T11:06:03Z', payload: { step_id: 'step-tool', tool_name: 'workspace_write_file', input_summary: 'reports/final.md · 5 B', output_summary: '成功 · reports/final.md · 5 B', output_excerpt: '{\n  "path": "reports/final.md"\n}' } },
      { id: 'event-4', run_id: 'run-1', stream_seq: 4, event_type: 'step.succeeded', occurred_at: '2026-07-20T11:06:04Z', payload: { step_id: 'step-tool', result_summary: '工具执行完成。' } },
    ]

    const timeline = buildTaskTimeline({ ...task, status: 'succeeded', error_code: '', result_summary: '完成' }, events)
    expect(timeline[1]).toMatchObject({
      label: '写入工作区文件',
      state: 'succeeded',
      detail: '成功 · reports/final.md · 5 B',
      tool: {
        name: 'workspace_write_file',
        input: 'reports/final.md · 5 B',
        result: '成功 · reports/final.md · 5 B',
      },
    })
    expect(timeline[1]?.tool?.output).toContain('reports/final.md')
  })

  it('uses the frozen MCP capability name for execution labels', () => {
    const events: RunEvent[] = [
      { id: 'event-1', run_id: 'run-1', stream_seq: 1, event_type: 'run.created', occurred_at: '2026-08-24T11:06:01Z', payload: {} },
      { id: 'event-2', run_id: 'run-1', stream_seq: 2, event_type: 'tool.planned', occurred_at: '2026-08-24T11:06:02Z', payload: { step_id: 'step-browser', tool_name: 'mcp__capability-id__browser_navigate', tool_display_name: '@playwright/mcp · browser_navigate' } },
      { id: 'event-3', run_id: 'run-1', stream_seq: 3, event_type: 'step.succeeded', occurred_at: '2026-08-24T11:06:03Z', payload: { step_id: 'step-browser', result_summary: '完成' } },
    ]

    const timeline = buildTaskTimeline({ ...task, status: 'succeeded', error_code: '', result_summary: '完成' }, events)
    expect(timeline[1]).toMatchObject({ label: 'Playwright MCP 打开网页', state: 'succeeded' })
  })

  it('presents an automatic compatibility retry as a model recovery step', () => {
    const events: RunEvent[] = [
      { id: 'event-1', run_id: 'run-1', stream_seq: 1, event_type: 'run.created', occurred_at: '2026-07-20T11:06:01Z', payload: {} },
      { id: 'event-2', run_id: 'run-1', stream_seq: 2, event_type: 'model.retry_queued', occurred_at: '2026-07-20T11:06:02Z', payload: { step_id: 'retry-step', retry_count: 1, max_retries: 3, error_code: 'PROVIDER_TOOL_ARGUMENTS_TRUNCATED' } },
      { id: 'event-3', run_id: 'run-1', stream_seq: 3, event_type: 'step.started', occurred_at: '2026-07-20T11:06:03Z', payload: { step_id: 'retry-step' } },
    ]
    const timeline = buildTaskTimeline({ ...task, status: 'running', error_code: '', result_summary: '' }, events)
    expect(timeline[1]).toMatchObject({ label: '修复模型响应（第 1/3 次）', state: 'running' })
    expect(taskFailureInfo({ error_code: 'PROVIDER_TOOL_ARGUMENTS_TRUNCATED', result_summary: '' })?.retryable).toBe(true)
  })

  it('turns a persisted model plan into numbered executable steps', () => {
    const events: RunEvent[] = [
      { id: 'event-1', run_id: 'run-1', stream_seq: 1, event_type: 'execution.plan_created', occurred_at: '2026-07-20T11:06:01Z', payload: { plan_id: 'plan-1', iteration: 2, summary: '先检索，再生成报告。', items: [
        { index: 1, step_id: 'step-search', tool_name: 'web_search', input_summary: 'Physical AI 2026' },
        { index: 2, step_id: 'step-file', tool_name: 'workspace_write_file', input_summary: 'reports/final.md · 12 KB' },
      ] } },
      { id: 'event-2', run_id: 'run-1', stream_seq: 2, event_type: 'tool.planned', occurred_at: '2026-07-20T11:06:02Z', payload: { step_id: 'step-search', tool_name: 'web_search' } },
      { id: 'event-3', run_id: 'run-1', stream_seq: 3, event_type: 'step.succeeded', occurred_at: '2026-07-20T11:06:03Z', payload: { step_id: 'step-search', result_summary: '完成' } },
      { id: 'event-4', run_id: 'run-1', stream_seq: 4, event_type: 'tool.planned', occurred_at: '2026-07-20T11:06:04Z', payload: { step_id: 'step-file', tool_name: 'workspace_write_file' } },
    ]
    const timeline = buildTaskTimeline({ ...task, status: 'running', error_code: '', result_summary: '' }, events)
    const plan = buildTaskExecutionPlan(events, timeline)
    expect(plan).toMatchObject({ iteration: 2, summary: '先检索，再生成报告。' })
    expect(plan?.items).toEqual([
      expect.objectContaining({ index: 1, label: '搜索互联网来源', state: 'succeeded' }),
      expect.objectContaining({ index: 2, label: '写入工作区文件', state: 'pending' }),
    ])
  })

  it('preserves the governed private fetch presentation without rendering HTML', () => {
    const events: RunEvent[] = [
      { id: 'event-fetch-1', run_id: 'run-1', stream_seq: 1, event_type: 'tool.completed', occurred_at: '2026-08-20T12:00:00Z', payload: {
        step_id: 'step-fetch', tool_name: 'web_fetch', input_summary: 'http://127.0.0.1:3000/docs', output_summary: '完成',
        presentation: { kind: 'web_fetch', network_class: 'private', rule_id: 'local-preview', url: 'http://127.0.0.1:3000/docs', status_code: 200, media_type: 'text/html', content_format: 'markdown', content_preview: '<script>never execute</script> # Docs', truncated: true },
      } },
      { id: 'event-fetch-2', run_id: 'run-1', stream_seq: 2, event_type: 'step.succeeded', occurred_at: '2026-08-20T12:00:01Z', payload: { step_id: 'step-fetch', result_summary: '完成' } },
    ]
    const timeline = buildTaskTimeline({ ...task, status: 'succeeded', error_code: '', result_summary: '完成' }, events)
    expect(timeline.find((item) => item.tool?.name === 'web_fetch')).toMatchObject({ label: '读取网页来源', tool: { name: 'web_fetch', presentation: {
      kind: 'web_fetch', network_class: 'private', rule_id: 'local-preview', status_code: 200, content_preview: '<script>never execute</script> # Docs', truncated: true,
    } } })
  })
})
