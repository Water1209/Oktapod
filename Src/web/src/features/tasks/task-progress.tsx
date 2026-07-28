import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { AlertTriangle, Check, CheckCircle2, ChevronDown, ChevronRight, Circle, Clock3, Files, LoaderCircle, RotateCw, Workflow } from 'lucide-react'
import { api, type RunEvent, type Task } from '../../shared/api/client'
import { buildTaskExecutionPlan, buildTaskTimeline, taskFailureInfo, taskStatusView, type TimelineState } from './task-progress-model'
import { ApprovalCard } from '../approvals/approval-inbox'
import { MultiFileReviewDialog } from './multi-file-review'
import { useRunEvents } from '../runs/use-run-events'

export function TaskTimeline({ task, events, compact = false }: { task: Task; events: RunEvent[]; compact?: boolean }) {
  const allItems = buildTaskTimeline(task, events)
  const items = allItems
  const plan = buildTaskExecutionPlan(events, allItems)
  return <>
    {plan ? <section className="task-execution-plan" aria-label={`执行计划，第 ${plan.iteration} 轮`}>
      <header><span><Check size={14} /><strong>执行计划</strong></span><small>第 {plan.iteration} 轮 · {plan.items.length} 步</small></header>
      {plan.summary ? <p>{plan.summary}</p> : null}
      <ol>{plan.items.map((item) => <li className={item.state} key={item.id}>
        <span className="task-plan-index">{item.index}</span>
        <span><strong>{item.label}</strong>{item.detail ? <small>{item.detail}</small> : null}</span>
        <em>{planStateLabel(item.state)}</em>
      </li>)}</ol>
    </section> : null}
    <ol className={`task-timeline ${compact ? 'compact' : ''}`} aria-label="执行步骤">
      {items.map((item) => <li className={item.state} key={item.id}>
      <span className="task-timeline-glyph" aria-hidden="true">{timelineIcon(item.state)}</span>
      <span className="task-timeline-copy">
        <span className="task-timeline-title"><strong>{item.label}</strong>{item.tool ? <code>{item.tool.name}</code> : null}</span>
        {item.tool ? <>
          {item.tool.input ? <small className="task-tool-input">{item.tool.input}</small> : null}
          <span className={`task-tool-result ${item.state}`}><span aria-hidden="true" />{item.tool.result || item.detail}</span>
          {item.tool.output ? <details className="task-tool-output"><summary>查看执行结果<ChevronDown size={12} /></summary><pre>{item.tool.output}</pre></details> : null}
        </> : <small>{item.detail}</small>}
      </span>
      <time dateTime={item.occurredAt}>{new Date(item.occurredAt).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}</time>
      </li>)}
    </ol>
  </>
}

export function ConversationTaskCard({ task, inStrip = false }: { task: Task; inStrip?: boolean }) {
  const queryClient = useQueryClient()
  const active = ['queued', 'running', 'waiting_approval', 'waiting_input'].includes(task.status)
  const events = useRunEvents(task.run_id, active)
  const artifacts = useQuery({ queryKey: ['run-artifacts', task.run_id], queryFn: () => api.runArtifacts(task.run_id), refetchInterval: active ? 1_000 : false })
  const approvals = useQuery({ queryKey: ['approvals', 'pending'], queryFn: () => api.approvals('pending'), enabled: task.status === 'waiting_approval', refetchInterval: task.status === 'waiting_approval' ? 1_500 : false })
  const [reviewOpen, setReviewOpen] = useState(false)
  const liveEvents = events.data.items
  const timelineItems = buildTaskTimeline(task, liveEvents)
  const executionPlan = buildTaskExecutionPlan(liveEvents, timelineItems)
  const failure = taskFailureInfo(task)
  const status = taskStatusView[task.status]
  const pendingApprovals = approvals.data?.items.filter((approval) => approval.task_id === task.id) ?? []
  const artifactCount = artifacts.data?.items.length ?? 0
  const completedSteps = timelineItems.filter((item) => item.state === 'succeeded').length
  const totalSteps = Math.max(executionPlan?.items.length ?? timelineItems.length, 1)
  const currentStep = Math.min(
    totalSteps,
    Math.max(1, timelineItems.findIndex((item) => ['running', 'failed', 'blocked'].includes(item.state)) + 1 || completedSteps),
  )

  const retry = useMutation({
    mutationFn: () => api.controlTask(task.id, 'retry'),
    onSuccess: async (updated) => {
      queryClient.setQueryData(['task', task.id], updated)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['tasks', task.project_id] }),
        queryClient.invalidateQueries({ queryKey: ['run-events', task.run_id] }),
        queryClient.invalidateQueries({ queryKey: ['conversation-messages', task.conversation_id] }),
      ])
    },
  })

  return <section className={`conversation-task-card-slim ${status.tone} ${inStrip ? 'in-strip' : ''}`} aria-label={`任务进展：${task.title}`}>
    <div className="task-step-indicator-wrap">
      <Link to="/tasks/$taskId" params={{ taskId: task.id }} className="task-step-indicator" title="进入任务详情">
        {active ? <LoaderCircle className="spin" size={14} /> : task.status === 'succeeded' ? <CheckCircle2 size={14} /> : task.status === 'failed' ? <AlertTriangle size={14} /> : <Circle size={14} />}
        <span className="task-row-title">{task.title}</span>
        <small className="task-row-status">{status.label}</small>
        <span className="task-row-progress">{currentStep} / {totalSteps}</span>
        <ChevronRight className="task-row-chevron" size={13} />
      </Link>

      {artifactCount > 0 ? <button type="button" className="task-row-files" onClick={() => setReviewOpen(true)} title={`审阅 ${artifactCount} 个文件`}>
        <Files size={13} /><span>{artifactCount} 个文件</span>{task.accepted ? <Check size={12} /> : null}
      </button> : null}

      <div className="task-step-popup" aria-label={`${task.title}执行进展`}>
        <div className="task-step-popup-header">
          <span><Workflow size={14} /><span>{task.title}</span></span>
          <span className="task-step-popup-status">{status.label}</span>
        </div>
        {pendingApprovals.length ? <div className="task-popup-approvals">{pendingApprovals.map((approval) => <ApprovalCard compact approval={approval} key={approval.id} />)}</div> : null}
        <TaskTimeline task={task} events={liveEvents} compact />
        <div className="task-step-popup-footer">
          <Link to="/tasks/$taskId" params={{ taskId: task.id }}>进入任务详情<ChevronRight size={13} /></Link>
        </div>
      </div>
    </div>

    {failure ? <div className="conversation-task-error-slim" role="alert"><AlertTriangle size={13} /><span><strong>{failure.title}</strong><small>{failure.detail}</small></span>{failure?.retryable ? <button type="button" className="task-error-retry" disabled={retry.isPending} onClick={() => retry.mutate()}><RotateCw size={12} />重试</button> : null}</div> : null}

    {artifacts.data?.items.length ? <MultiFileReviewDialog task={task} artifacts={artifacts.data.items} open={reviewOpen} onOpenChange={setReviewOpen} /> : null}
    {retry.error ? <div className="conversation-task-action-error">{retry.error.message}</div> : null}
  </section>
}

function timelineIcon(state: TimelineState) {
  if (state === 'succeeded') return <Check size={13} />
  if (state === 'running') return <LoaderCircle className="spin" size={13} />
  if (state === 'failed' || state === 'blocked') return <AlertTriangle size={13} />
  if (state === 'pending') return <Clock3 size={13} />
  return <Circle size={12} />
}

function planStateLabel(state: TimelineState) {
  if (state === 'succeeded') return '完成'
  if (state === 'running') return '执行中'
  if (state === 'failed' || state === 'blocked') return '修复中'
  return '待执行'
}
