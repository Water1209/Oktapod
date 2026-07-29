import type { RunEvent, Task, TaskStatus } from '../../shared/api/client'

export const taskStatusView: Record<TaskStatus, { label: string; tone: string }> = {
  queued: { label: '等待执行', tone: 'pending' },
  running: { label: '执行中', tone: 'running' },
  waiting_approval: { label: '等待审批', tone: 'warning' },
  waiting_input: { label: '等待输入', tone: 'warning' },
  paused: { label: '已暂停', tone: 'paused' },
  blocked: { label: '执行受阻', tone: 'failed' },
  succeeded: { label: '已完成', tone: 'succeeded' },
  failed: { label: '执行失败', tone: 'failed' },
  canceled: { label: '已取消', tone: 'muted' },
}

export interface TaskFailureInfo {
  title: string
  detail: string
  action: string
  retryable: boolean
}

export function taskFailureInfo(task: Pick<Task, 'error_code' | 'result_summary'>): TaskFailureInfo | undefined {
  const code = task.error_code?.trim()
  if (!code) return undefined
  const summary = task.result_summary?.trim() ?? ''
  const timedOut = /context deadline exceeded|Client\.Timeout exceeded|awaiting headers/i.test(summary)

  if (code === 'PROVIDER_UNAVAILABLE') {
    return timedOut
      ? { title: '模型服务请求超时', detail: '等待模型响应超过 120 秒，本次模型交互没有完成。', action: '系统会从当前检查点自动重试，连续 3 次失败后才停止。', retryable: true }
      : { title: '模型服务暂时不可用', detail: '当前请求没有获得可用响应，已保留现有执行进度。', action: '系统会从当前检查点自动重试，连续 3 次失败后才停止。', retryable: true }
  }
  if (code === 'PROVIDER_AUTH') return { title: '模型凭证被拒绝', detail: '模型服务未接受当前凭证，任务没有继续执行。', action: '更新受保护的模型凭证后重试。', retryable: true }
  if (code === 'MODEL_CREDENTIAL_MISSING') return { title: '数据库中没有可用的模型配置', detail: '当前工作空间尚未保存模型配置，或对应系统凭据不可访问。', action: '运行 oktapod model configure 后直接重试。', retryable: true }
  if (code === 'BUDGET_RESERVATION_EXCEEDED' || code === 'BUDGET_EXCEEDED') return { title: '本次运行配额不足', detail: '任务下一步需要的 Token 或模型调用次数已超过本次 Run 的剩余配额，系统已保留当前检查点。', action: '请在任务详情增加本次 Run 的配额；保存后系统会从检查点重试。', retryable: false }
  if (code === 'PROVIDER_RATE_LIMIT') return { title: '模型服务请求过多', detail: 'Provider 暂时拒绝了本次请求，现有步骤不会丢失。', action: '系统会自动重试，连续 3 次失败后才停止。', retryable: true }
  if (code === 'PROVIDER_MODEL_NOT_FOUND') return { title: '所选模型不可用', detail: 'Provider 不支持当前任务冻结的模型。', action: '修改项目模型后重新创建任务。', retryable: false }
  if (code === 'PROVIDER_PROTOCOL_ERROR' || code === 'PROVIDER_EMPTY_RESPONSE') return { title: '模型返回内容不完整', detail: '响应无法解析或没有可用内容，现有步骤不会丢失。', action: '系统会自动修复并重试，连续 3 次失败后才停止。', retryable: true }
  if (code === 'PROVIDER_TOOL_ARGUMENTS_INVALID') return { title: '模型工具参数格式异常', detail: '模型生成的工具参数不是完整 JSON，系统没有执行这次工具调用。', action: '系统会自动修复并重试，连续 3 次失败后才停止。', retryable: true }
  if (code === 'PROVIDER_TOOL_ARGUMENTS_TRUNCATED') return { title: '模型工具参数被截断', detail: '模型没有完整输出工具参数，系统已拒绝执行不完整动作。', action: '系统会拆分长参数并重试，连续 3 次失败后才停止。', retryable: true }
  if (code === 'SCENE_POLICY_DENIED') return { title: '当前场景不允许该操作', detail: '任务规划了当前场景禁止的命令，相关动作没有执行，已完成步骤仍会保留。', action: '从当前检查点重试，系统会改用本场景允许的工具重新规划。', retryable: true }
  if (code === 'TOOL_POLICY_DENIED') return { title: '工具动作被安全策略拒绝', detail: '冻结的工具或参数不在允许范围内，相关动作没有执行。', action: '修订目标或参数后重新创建任务。', retryable: false }
  if (code === 'SIDE_EFFECT_STATUS_UNKNOWN') return { title: '外部操作结果未知', detail: '系统无法确认上一次非幂等操作是否完成，因此停止自动重试。', action: '先核查外部状态，再在任务详情中确认处理方式。', retryable: false }
  if (code === 'COMMAND_EXIT_NONZERO') return { title: '命令执行失败', detail: '命令已经运行，但退出码不是 0。', action: '查看工具事件和输出，修订命令后重试。', retryable: true }
  if (code === 'WEB_EVIDENCE_MISSING') return { title: '检索证据不足', detail: '任务要求使用最新互联网资料，但当前 Run 没有任何成功的检索收据，因此未标记为完成。', action: '检查网络后从阻塞点继续，或明确选择基于已有资料交付。', retryable: true }
  if (code === 'DELIVERABLE_MISSING') return { title: '交付文件尚未生成', detail: '任务要求生成 Markdown 或文档，但当前 Run 没有成功写入工作区文件，因此未标记为完成。', action: '从阻塞点继续，系统会补写并验证交付文件。', retryable: true }
  if (code === 'RUN_PLAN_PREMATURE_COMPLETION') return { title: '执行计划状态无效', detail: '初次计划把尚未执行的工作提前标记为完成，系统已拒绝该状态。', action: '从当前检查点重试，重新生成从第一项开始的计划。', retryable: true }
  if (code === 'RUN_PLAN_STATUS_REGRESSION') return { title: '计划更新受阻', detail: '模型尝试把已经完成的计划项改回未完成状态；系统保留了真实进度并拒绝这次更新。', action: '当前版本会先自动修复，连续 3 次失败才停止；此任务也可从阻塞点继续。', retryable: true }
  if (code === 'APPROVAL_REJECTED' || code === 'APPROVAL_EXPIRED') return { title: code === 'APPROVAL_EXPIRED' ? '审批已过期' : '审批未通过', detail: '风险动作没有执行，任务已停止。', action: '修订动作后重新发起任务。', retryable: false }
  return { title: '任务未能完成', detail: summary || `运行在 ${code} 处停止。`, action: '查看执行步骤和技术事件后再决定是否重试。', retryable: false }
}

export type TimelineState = 'pending' | 'running' | 'succeeded' | 'failed' | 'blocked' | 'muted'

export interface TaskTimelineItem {
  id: string
  label: string
  detail: string
  state: TimelineState
  occurredAt: string
  code?: string
  tool?: {
    name: string
    input: string
    result: string
    output: string
  }
}

export interface TaskExecutionPlanItem {
  id: string
  index: number
  label: string
  detail: string
  state: TimelineState
}

export interface TaskExecutionPlan {
  id: string
  iteration: number
  summary: string
  items: TaskExecutionPlanItem[]
}

const terminalEvents = new Set(['step.succeeded', 'step.failed', 'step.blocked'])

export function buildTaskTimeline(task: Task, events: RunEvent[]): TaskTimelineItem[] {
  const ordered = [...events].sort((a, b) => a.stream_seq - b.stream_seq)
  const items: TaskTimelineItem[] = []
  const created = ordered.find((event) => event.event_type === 'run.created')
  items.push({ id: created?.id ?? `created-${task.id}`, label: '任务已创建', detail: task.mode === 'agent_team' ? '已建立 Agent Team 执行计划。' : '已进入受控执行队列。', state: 'succeeded', occurredAt: created?.occurred_at ?? task.created_at })

  const groups = new Map<string, RunEvent[]>()
  for (const event of ordered) {
    const stepID = payloadString(event, 'step_id')
    if (!stepID || (!event.event_type.startsWith('step.') && !event.event_type.startsWith('tool.') && !event.event_type.startsWith('approval.') && !event.event_type.startsWith('model.'))) continue
    const current = groups.get(stepID) ?? []
    current.push(event)
    groups.set(stepID, current)
  }

  for (const [stepID, stepEvents] of groups) {
    const latest = stepEvents.at(-1)!
    const final = [...stepEvents].reverse().find((event) => terminalEvents.has(event.event_type))
    const toolEvent = stepEvents.find((event) => event.event_type.startsWith('tool.'))
    const toolCompleted = [...stepEvents].reverse().find((event) => event.event_type === 'tool.completed')
    const toolName = (toolCompleted ? payloadString(toolCompleted, 'tool_name') : '') || (toolEvent ? payloadString(toolEvent, 'tool_name') : '')
    const toolInput = [...stepEvents].reverse().map((event) => payloadString(event, 'input_summary')).find(Boolean) ?? ''
    const toolResult = [...stepEvents].reverse().map((event) => payloadString(event, 'output_summary')).find(Boolean) ?? ''
    const toolOutput = [...stepEvents].reverse().map((event) => payloadString(event, 'output_excerpt')).find(Boolean) ?? ''
    const modelProgress = [...stepEvents].reverse().map((event) => payloadString(event, 'content')).find(Boolean) ?? ''
    const code = final ? payloadString(final, 'error_code') : ''
    let state: TimelineState = 'pending'
    if (final?.event_type === 'step.succeeded') state = 'succeeded'
    else if (final?.event_type === 'step.failed') state = 'failed'
    else if (final?.event_type === 'step.blocked') state = 'blocked'
    else if (stepEvents.some((event) => event.event_type === 'step.started' || event.event_type === 'tool.execution_started')) state = 'running'
    if (!final && ['succeeded', 'failed', 'blocked', 'canceled'].includes(task.status)) state = 'muted'

    const failure = code ? taskFailureInfo({ error_code: code, result_summary: payloadString(final!, 'result_summary') || task.result_summary }) : undefined
    const label = toolName ? toolStepLabel(toolName) : modelStepLabel(stepEvents, groups.size)
    let detail = state === 'muted' ? '任务已经结束，这个排队步骤没有执行。' : '已进入执行队列，等待系统开始。'
    if (state === 'running') detail = toolName ? toolInput || '工具正在受控环境中执行。' : modelProgress || '正在分析目标并生成下一步动作。'
    if (state === 'succeeded') detail = toolResult || modelProgress || payloadString(final!, 'result_summary') || (toolName ? '工具动作已完成并记录收据。' : '分析与执行步骤已完成。')
    if (state === 'failed' || state === 'blocked') detail = toolResult || modelProgress || failure?.detail || payloadString(final!, 'result_summary') || '步骤已停止，请查看错误信息。'
    items.push({
      id: stepID,
      label,
      detail,
      state,
      occurredAt: latest.occurred_at,
      code: code || undefined,
      tool: toolName ? { name: toolName, input: toolInput, result: toolResult, output: toolOutput } : undefined,
    })
  }

  if (groups.size === 0 && task.status !== 'queued') {
    const failure = taskFailureInfo(task)
    items.push({ id: `status-${task.id}`, label: failure?.title ?? taskStatusView[task.status].label, detail: failure?.detail ?? '暂无可用的步骤事件。', state: task.status === 'succeeded' ? 'succeeded' : task.status === 'failed' ? 'failed' : task.status === 'blocked' ? 'blocked' : 'muted', occurredAt: task.updated_at, code: task.error_code })
  }
  return items
}

export function buildTaskExecutionPlan(events: RunEvent[], timeline: TaskTimelineItem[]): TaskExecutionPlan | undefined {
  const planEvent = [...events]
    .sort((left, right) => left.stream_seq - right.stream_seq)
    .filter((event) => event.event_type === 'execution.plan_created')
    .at(-1)
  if (!planEvent || !Array.isArray(planEvent.payload.items)) return undefined
  const items = planEvent.payload.items.flatMap((raw, position) => {
    if (!raw || typeof raw !== 'object') return []
    const value = raw as Record<string, unknown>
    const stepID = typeof value.step_id === 'string' ? value.step_id : ''
    const toolName = typeof value.tool_name === 'string' ? value.tool_name : ''
    if (!stepID || !toolName) return []
    const current = timeline.find((item) => item.id === stepID)
    return [{
      id: stepID,
      index: typeof value.index === 'number' ? value.index : position + 1,
      label: toolStepLabel(toolName),
      detail: typeof value.input_summary === 'string' ? value.input_summary : '',
      state: current?.state ?? 'pending',
    } satisfies TaskExecutionPlanItem]
  })
  if (!items.length) return undefined
  return {
    id: payloadString(planEvent, 'plan_id') || planEvent.id,
    iteration: payloadNumber(planEvent, 'iteration') || 1,
    summary: payloadString(planEvent, 'summary'),
    items,
  }
}

function payloadString(event: RunEvent, key: string) {
  const value = event.payload[key]
  return typeof value === 'string' ? value.trim() : ''
}

function modelStepLabel(events: RunEvent[], stepCount: number) {
	const toolRecovery = events.find((event) => event.event_type === 'tool.recovery_queued')
  if (toolRecovery) {
    const maxRetries = payloadNumber(toolRecovery, 'max_retries') || 3
    return `修复失败步骤（第 ${payloadNumber(toolRecovery, 'retry_count') || 1}/${maxRetries} 次）`
  }
  const retry = events.find((event) => event.event_type === 'model.retry_queued')
	if (retry) {
		const code = payloadString(retry, 'error_code')
    const maxRetries = payloadNumber(retry, 'max_retries') || 3
		const action = code === 'PROVIDER_TOOL_ARGUMENTS_INVALID' || code === 'PROVIDER_TOOL_ARGUMENTS_TRUNCATED' ? '修复模型响应' : '重新连接模型'
		return `${action}（第 ${payloadNumber(retry, 'retry_count') || 1}/${maxRetries} 次）`
	}
  const role = events.map((event) => payloadString(event, 'role')).find(Boolean)
  const stage = events.map((event) => payloadString(event, 'stage')).find(Boolean)
  if (stage === 'plan') return role === 'coordinator' ? '协调者拆分任务' : '分析目标并制定计划'
  if (stage === 'synthesis') return '汇总子任务结果'
  if (role) return `${roleLabel(role)}执行子任务`
  return stepCount > 1 ? '生成下一步执行计划' : '分析目标并制定执行计划'
}

function payloadNumber(event: RunEvent, key: string) {
  const value = event.payload[key]
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

function roleLabel(role: string) {
  const labels: Record<string, string> = { coordinator: '协调者', researcher: '研究者', builder: '构建者', reviewer: '审查者' }
  return labels[role] ?? role
}

function toolStepLabel(toolName: string) {
  const labels: Record<string, string> = { workspace_write_file: '写入工作区文件', workspace_read_file: '读取工作区文件', workspace_list_files: '检查工作区文件', workspace_run_command: '运行验证命令' }
  return labels[toolName] ?? `执行工具：${toolName}`
}
