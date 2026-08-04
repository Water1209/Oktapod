import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, CheckCircle2, ChevronUp, Circle, LoaderCircle, MoreHorizontal, PauseCircle, RotateCw, ShieldAlert } from 'lucide-react'
import { api, type Task } from '../../shared/api/client'
import { taskFailureInfo, taskStatusView } from './task-progress-model'
import { useRunEvents } from '../runs/use-run-events'
import { HumanActionCard } from '../approvals/human-action-card'

const activeStatuses = new Set(['queued', 'running', 'waiting_approval', 'waiting_input', 'paused'])

// 每个会话只呈现一个活动语义计划。工具事件属于计划项详情，不再被拼成动态“任务步骤”。
export function TaskStatusStrip({ activeTask, conversationId }: { activeTask?: Task | null; conversationId?: string }) {
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const actionRef = useRef<HTMLElement>(null)
  const task = activeTask ?? null
  const active = task && activeStatuses.has(task.status) ? task : null
  const terminal = task && !active ? task : null
  const events = useRunEvents(active?.run_id, Boolean(active))
  const plan = useQuery({
    queryKey: ['run-plan', task?.run_id],
    queryFn: () => api.runPlan(task!.run_id),
    enabled: Boolean(task?.run_id && task.status !== 'canceled'),
  })
  const refetchPlan = plan.refetch
  const actions = useQuery({
    queryKey: ['human-actions', conversationId, active?.run_id, 'active'],
    queryFn: () => api.humanActions({ conversationId, runId: active!.run_id }),
    enabled: Boolean(active?.run_id && conversationId),
  })
  const queue = useQuery({
    queryKey: ['conversation-queue', conversationId, 'pending'],
    queryFn: () => api.conversationQueue(conversationId!, 'pending'),
    enabled: Boolean(conversationId) && Boolean(active),
    refetchInterval: active ? 2000 : false,
  })
  const steer = useMutation({
    mutationFn: (itemId: string) => api.steerConversation(active!.conversation_id, itemId),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['conversation-queue', conversationId, 'pending'] })
      await queryClient.invalidateQueries({ queryKey: ['run-events', active?.run_id] })
    },
  })
  const withdraw = useMutation({
    mutationFn: (itemId: string) => api.cancelConversationQueueItem(active!.conversation_id, itemId),
    onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['conversation-queue', conversationId, 'pending'] }),
  })
  const retry = useMutation({
    mutationFn: () => api.controlTask(task!.id, 'retry'),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['tasks', task?.project_id] })
      await queryClient.invalidateQueries({ queryKey: ['task', task?.id] })
      await queryClient.invalidateQueries({ queryKey: ['run-plan', task?.run_id] })
    },
  })

  const latestPlanOrActionSeq = useMemo(
    () => events.data.items.reduce(
      (latest, event) => event.event_type.startsWith('plan.') || event.event_type.startsWith('human_action.')
        ? Math.max(latest, event.stream_seq)
        : latest,
      0,
    ),
    [events.data.items],
  )
  useEffect(() => {
    if (latestPlanOrActionSeq === 0) return
    void queryClient.invalidateQueries({ queryKey: ['run-plan', active?.run_id] })
    void queryClient.invalidateQueries({ queryKey: ['human-actions', conversationId, active?.run_id] })
  }, [active?.run_id, conversationId, latestPlanOrActionSeq, queryClient])
  useEffect(() => {
    if (!task?.run_id) return
    void refetchPlan()
  }, [refetchPlan, task?.run_id, task?.status])

  const pendingActions = useMemo(
    () => (actions.data?.items ?? []).filter((item) => item.status === 'waiting_user' || item.status === 'reviewing'),
    [actions.data?.items],
  )
  useEffect(() => {
    if (pendingActions.length === 0) return
    const frame = window.requestAnimationFrame(() => {
      setOpen(true)
      window.requestAnimationFrame(() => actionRef.current?.focus())
    })
    return () => window.cancelAnimationFrame(frame)
  }, [pendingActions.length])

  // The Composer-adjacent capsule represents current actionable work. Once a
  // task is canceled, its durable history remains in the message execution
  // record and sidebar, but the stale 0/N capsule must leave the input area.
  if (!task || task.status === 'canceled') return null

  const planItems = plan.data?.items ?? []
  const completed = planItems.filter((item) => item.status === 'completed').length
  const total = planItems.length
  const current = planItems.find((item) => ['running', 'waiting_user', 'blocked'].includes(item.status))
    ?? planItems.find((item) => item.status === 'pending')
  const failure = terminal ? taskFailureInfo(terminal) : undefined
  const currentLabel = failure?.title ?? current?.title ?? taskStatusView[task.status].label
  const pendingQueue = queue.data?.items ?? []
  const statusLabel = taskStatusView[task.status].label
  const paused = task.status === 'paused'
  const blocked = task.status === 'blocked' || task.status === 'failed'
  const steerError = steer.error ? (steer.error instanceof Error ? steer.error.message : '引导失败') : null

  return (
    <section
      className={`task-capsule ${open ? 'open' : ''} ${terminal ? `terminal ${task.status}` : ''}`}
      aria-label="当前执行计划"
    >
      <button type="button" className="task-capsule-head" onClick={() => setOpen((value) => !value)} aria-expanded={open}>
        <span className={`task-capsule-icon ${paused ? 'paused' : ''} ${blocked ? 'blocked' : ''}`} aria-hidden="true">
          {blocked ? <AlertTriangle size={13} /> : task.status === 'succeeded' ? <CheckCircle2 size={13} /> : paused ? <PauseCircle size={14} /> : <LoaderCircle className="spin" size={14} />}
        </span>
        <span className="task-capsule-current" aria-live="polite">{pendingActions.length > 0 ? '等待你处理' : currentLabel}</span>
        {total > 0 ? <span className="task-capsule-progress">{completed}/{total}</span> : null}
        {pendingActions.length > 0 ? <span className="task-capsule-action-badge"><ShieldAlert size={11} />{pendingActions.length}</span> : null}
        {pendingQueue.length > 0 ? <span className="task-capsule-queue-badge">队列 {pendingQueue.length}</span> : null}
        <ChevronUp size={13} className="task-capsule-chevron" aria-hidden="true" />
      </button>
      {open ? (
        <div className="task-capsule-detail">
          <header className="task-capsule-detail-head">
            <span className="task-capsule-detail-title">执行计划 · 第 {plan.data?.revision ?? 1} 版</span>
            <span className="task-capsule-detail-status">{statusLabel}</span>
          </header>
          {failure ? <div className="task-capsule-terminal">
            <p>{failure.detail}</p>
            <small>{failure.action}</small>
            {failure.retryable ? <button type="button" disabled={retry.isPending} onClick={() => retry.mutate()}><RotateCw size={12} />{retry.isPending ? '正在恢复' : '从阻塞点继续'}</button> : null}
          </div> : null}
          {planItems.length > 0 ? (
            <ol className="task-capsule-plan">
              {planItems.map((item) => (
                <li key={item.id} className={item.status}>
                  <span className="task-capsule-plan-icon" aria-hidden="true">
                    {item.status === 'completed' ? <CheckCircle2 size={12} /> : item.status === 'running' ? <LoaderCircle className="spin" size={12} /> : item.status === 'waiting_user' ? <PauseCircle size={12} /> : <Circle size={8} />}
                  </span>
                  <span className="task-capsule-plan-label"><strong>{item.title}</strong>{item.description ? <small>{item.description}</small> : null}</span>
                </li>
              ))}
            </ol>
          ) : (
            <p className="task-capsule-detail-current">{plan.isLoading ? '正在形成完整执行计划…' : currentLabel}</p>
          )}
          {pendingActions.length > 0 ? <section ref={actionRef} tabIndex={-1} className="human-action-stack" aria-label="需要你处理">
            {pendingActions.map((action) => <HumanActionCard action={action} key={action.id} />)}
          </section> : null}
          {pendingQueue.length > 0 ? (
            <div className="task-capsule-queue">
              <div className="task-capsule-queue-head">排队中 · {pendingQueue.length}</div>
              {pendingQueue.map((item) => (
                <div key={item.id} className="task-capsule-queue-row">
                  <span className="task-capsule-queue-content" title={item.content}><small>{item.kind === 'steering' ? '引导当前任务' : '下一轮'} · {new Date(item.created_at).toLocaleTimeString('zh-CN')}</small>{item.content}</span>
                  <details className="task-capsule-queue-menu" onClick={(event) => event.stopPropagation()}>
                    <summary aria-label="排队项操作" title="排队项操作"><MoreHorizontal size={14} /></summary>
                    <div role="menu">
                      {item.kind !== 'steering' ? <button type="button" role="menuitem" disabled={steer.isPending} onClick={() => steer.mutate(item.id)}>改为引导当前任务</button> : null}
                      <button className="danger" type="button" role="menuitem" disabled={withdraw.isPending} onClick={() => withdraw.mutate(item.id)} aria-label="撤回排队项">撤回</button>
                    </div>
                  </details>
                </div>
              ))}
            </div>
          ) : null}
          {steerError || withdraw.error || retry.error ? <div className="task-capsule-error" role="alert"><AlertTriangle size={12} />{steerError || withdraw.error?.message || retry.error?.message}</div> : null}
        </div>
      ) : null}
    </section>
  )
}
