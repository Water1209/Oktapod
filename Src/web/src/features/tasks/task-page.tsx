import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { AlertTriangle, ArrowLeft, Check, CheckCircle2, CircleDashed, ExternalLink, FileUp, Pause, Play, Radio, RotateCw, ShieldCheck, UsersRound, XCircle } from 'lucide-react'
import { api, type AgentTeamRoom, type Artifact, type RunEvent, type SideEffectReceipt, type Task, type Visibility } from '../../shared/api/client'
import { ApprovalCard } from '../approvals/approval-inbox'
import { TaskTimeline } from './task-progress'
import { buildTaskTimeline, taskFailureInfo, taskStatusView } from './task-progress-model'
import { RunBudgetControl } from './run-budget-control'
import { FileReviewProvider } from './multi-file-review'
import { MarkdownPreviewDialog } from '../../shared/markdown-preview'
import { useRunEvents } from '../runs/use-run-events'

export function TaskPage() {
  const { taskId } = useParams({ from: '/tasks/$taskId' })
  const task = useQuery({ queryKey: ['task', taskId], queryFn: () => api.task(taskId), refetchInterval: (query) => query.state.data?.status === 'running' || query.state.data?.status === 'queued' ? 1_000 : false })
  const usage = useQuery({ queryKey: ['run-usage', task.data?.run_id], queryFn: () => api.runUsage(task.data!.run_id), enabled: !!task.data?.run_id, refetchInterval: (query) => {
    const status = task.data?.status
    if (status === 'running' || status === 'queued') return 1_000
    // Terminal task data and the usage ledger are committed in separate HTTP
    // reads. Keep a short bounded convergence window so the transparency card
    // never stays at a stale zero after a successful model call.
    if (status === 'succeeded' && (query.state.data?.model_calls ?? 0) === 0) return 500
    return false
  } })
  const artifacts = useQuery({ queryKey: ['run-artifacts', task.data?.run_id], queryFn: () => api.runArtifacts(task.data!.run_id), enabled: !!task.data?.run_id })
  const agentTeam = useQuery({
    queryKey: ['agent-team-room', task.data?.run_id],
    queryFn: () => api.agentTeamRoom(task.data!.run_id),
    enabled: task.data?.mode === 'agent_team' && !!task.data?.run_id,
    refetchInterval: task.data?.status === 'running' || task.data?.status === 'queued' ? 1_000 : false,
  })
  const approvals = useQuery({ queryKey: ['approvals', 'pending'], queryFn: () => api.approvals('pending'), enabled: task.data?.status === 'waiting_approval' })
  const sideEffects = useQuery({ queryKey: ['side-effect-receipts', 'unknown'], queryFn: () => api.sideEffectReceipts('unknown'), enabled: task.data?.status === 'blocked' || task.data?.status === 'failed' })
  const queryClient = useQueryClient()
  const liveEventQuery = useRunEvents(task.data?.run_id, Boolean(task.data && ['queued', 'running', 'waiting_approval', 'waiting_input', 'paused'].includes(task.data.status)))
  const liveEvents = liveEventQuery.data.items
  const control = useMutation({
    mutationFn: (command: 'pause' | 'resume' | 'cancel' | 'retry') => api.controlTask(taskId, command),
    onSuccess: async (updated) => {
      queryClient.setQueryData(['task', taskId], updated)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['task', taskId] }),
        queryClient.invalidateQueries({ queryKey: ['tasks', updated.project_id] }),
        queryClient.invalidateQueries({ queryKey: ['run-events', updated.run_id] }),
        queryClient.invalidateQueries({ queryKey: ['conversation-messages', updated.conversation_id] }),
      ])
    },
  })
  const acceptOutcome = useMutation({
    mutationFn: () => api.acceptTaskOutcome(taskId),
    onSuccess: () => {
      queryClient.setQueryData<Task | undefined>(['task', taskId], (current) => current ? { ...current, accepted: true } : current)
      void queryClient.invalidateQueries({ queryKey: ['task', taskId] })
    },
  })
  const resolveSideEffect = useMutation({
    mutationFn: ({ receiptId, decision, note }: { receiptId: string; decision: 'confirm_applied' | 'confirm_not_applied' | 'abandon'; note: string }) => api.resolveSideEffect(receiptId, decision, note),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['side-effect-receipts', 'unknown'] })
      await queryClient.invalidateQueries({ queryKey: ['task', taskId] })
      await queryClient.invalidateQueries({ queryKey: ['conversation-messages', conversationID] })
      await queryClient.invalidateQueries({ queryKey: ['run-events', runID] })
    },
  })

  const runID = task.data?.run_id
  const conversationID = task.data?.conversation_id
  const model = [...liveEvents].reverse().map((event) => event.payload.model).find((value): value is string => typeof value === 'string' && value.trim() !== '')
  const pendingApprovals = approvals.data?.items.filter((approval) => approval.task_id === taskId) ?? []
  const unknownSideEffects = sideEffects.data?.items.filter((receipt) => receipt.task_id === taskId && receipt.status === 'unknown') ?? []

  if (task.isLoading) return <div className="page-loading">正在恢复任务现场…</div>
  if (task.error || !task.data) return <div className="empty-panel"><h1>任务无法打开</h1><p>{task.error?.message ?? '任务不存在或当前不可见。'}</p></div>
  const status = taskStatusView[task.data.status]
  const failure = taskFailureInfo(task.data)
  const timeline = buildTaskTimeline(task.data, liveEvents)
  const modeLabel = task.data.mode === 'agent_team' ? 'Agent Team' : task.data.mode === 'single_agent' ? '单智能体' : task.data.mode === 'answer' ? '直接回答' : '自动'
  const actionError = control.error?.message ?? acceptOutcome.error?.message

  return <FileReviewProvider><div className="task-detail-workspace">
    <header className="task-detail-nav">
      <Link to="/projects/$projectId" params={{ projectId: task.data.project_id }} search={{ conversation: task.data.conversation_id }}><ArrowLeft size={15} />返回项目会话</Link>
      <span>任务 {task.data.id.slice(0, 8)}</span>
    </header>

    <main className="task-detail-content">
      <section className="task-summary-panel" aria-labelledby="task-detail-title">
        <header className="task-summary-heading">
          <div>
            <span className={`task-status-badge ${status.tone}`}>{status.label}</span>
            <h1 id="task-detail-title">{task.data.title}</h1>
            <p>{task.data.goal}</p>
          </div>
          <div className="task-detail-actions" aria-label="任务操作">
            {(task.data.status === 'queued' || task.data.status === 'running') ? <button type="button" disabled={control.isPending} onClick={() => control.mutate('pause')}><Pause size={14} />暂停</button> : null}
            {task.data.status === 'paused' ? <button type="button" disabled={control.isPending} onClick={() => control.mutate('resume')}><Play size={14} />继续</button> : null}
            {(task.data.status === 'failed' || task.data.status === 'blocked') && failure?.retryable ? <button className="primary" type="button" disabled={control.isPending} onClick={() => control.mutate('retry')}><RotateCw size={14} />{control.isPending ? '恢复中' : '恢复任务'}</button> : null}
            {!['succeeded', 'failed', 'canceled'].includes(task.data.status) ? <button type="button" disabled={control.isPending} onClick={() => control.mutate('cancel')}><XCircle size={14} />取消</button> : null}
            {task.data.status === 'succeeded' && !task.data.accepted ? <button className="primary" type="button" disabled={acceptOutcome.isPending} onClick={() => acceptOutcome.mutate()}><Check size={14} />接受成果</button> : null}
          </div>
        </header>

        {failure ? <div className="task-failure-summary" role="alert"><AlertTriangle size={18} /><span><strong>{failure.title}</strong><p>{failure.detail}</p><small>{failure.action}</small></span><code>{task.data.error_code}</code></div> : null}
        {actionError ? <div className="task-action-error" role="alert">{actionError}</div> : null}

        <dl className="task-info-grid" aria-label="任务信息">
          <div><dt>运行</dt><dd>{task.data.run_id.slice(0, 8)}</dd></div>
          <div><dt>执行方式</dt><dd>{modeLabel}</dd></div>
          <div><dt>模型</dt><dd>{model || '尚未调用'}</dd></div>
          <div><dt>开始时间</dt><dd>{new Date(task.data.created_at).toLocaleString('zh-CN')}</dd></div>
          <div><dt>执行耗时</dt><dd>{formatDuration(task.data.created_at, task.data.updated_at)}</dd></div>
          <div><dt>模型用量</dt><dd>{usage.data ? `${usage.data.model_calls} 次 · ${(usage.data.input_tokens + usage.data.output_tokens).toLocaleString()} Token` : '读取中'}</dd></div>
        </dl>
      </section>

      {task.data.error_code === 'BUDGET_RESERVATION_EXCEEDED' && usage.data ? <RunBudgetControl taskId={task.data.id} runId={task.data.run_id} taskStatus={task.data.status} usage={usage.data} /> : null}

      <section className="task-progress-panel" aria-labelledby="task-progress-heading">
        <header><div><strong id="task-progress-heading">执行步骤</strong><span>大任务已拆分出的实际 Run/Step/Tool 过程</span></div><em>{timeline.length} 个步骤</em></header>
        <TaskTimeline task={task.data} events={liveEvents} />
      </section>

      {pendingApprovals.length || unknownSideEffects.length ? <section className="task-exception-section" aria-label="待处理事项">
        {pendingApprovals.length ? <div className="inline-approvals">{pendingApprovals.map((approval) => <ApprovalCard compact approval={approval} key={approval.id} />)}</div> : null}
        {unknownSideEffects.length ? <UnknownSideEffectPanel receipts={unknownSideEffects} pending={resolveSideEffect.isPending} error={resolveSideEffect.error} onResolve={(receiptId, decision, note) => resolveSideEffect.mutate({ receiptId, decision, note })} /> : null}
      </section> : null}

      {task.data.status === 'succeeded' && task.data.result_summary ? <section className="task-result-panel" aria-label="任务结果"><header><ShieldCheck size={16} /><strong>任务结果</strong>{task.data.accepted ? <span><Check size={13} />已接受</span> : null}</header><div>{task.data.result_summary}</div></section> : null}

      {artifacts.data?.items.length ? <section className="task-artifact-panel" aria-label="任务产物"><header><strong>任务产物</strong><span>{artifacts.data.items.length} 项</span></header><div className="artifact-strip">{artifacts.data.items.map((artifact) => <ArtifactPublishRow artifact={artifact} projectId={task.data.project_id} key={artifact.id} />)}</div></section> : null}

      {task.data.mode === 'agent_team' ? <AgentTeamPanel room={agentTeam.data} loading={agentTeam.isLoading} error={agentTeam.error} /> : null}

      <details className="task-technical-details">
        <summary><span><Radio size={15} />技术事件和诊断信息</span><em>{liveEvents.length} 条 · cursor {liveEvents.at(-1)?.stream_seq ?? 0}</em></summary>
        {task.data.error_code ? <div className="task-technical-error"><strong>{task.data.error_code}</strong><code>{task.data.result_summary}</code></div> : null}
        <div className="event-list">{liveEvents.map((event) => <EventRow event={event} key={event.id} />)}{liveEvents.length === 0 ? <p className="event-empty">暂无运行事件。</p> : null}</div>
      </details>
    </main>
  </div>
  </FileReviewProvider>
}

function UnknownSideEffectPanel({ receipts, pending, error, onResolve }: { receipts: SideEffectReceipt[]; pending: boolean; error: Error | null; onResolve: (receiptId: string, decision: 'confirm_applied' | 'confirm_not_applied' | 'abandon', note: string) => void }) {
  const [notes, setNotes] = useState<Record<string, string>>({})
  return <div className="unknown-side-effect-panel" role="alert">
    <div className="unknown-side-effect-heading"><AlertTriangle size={18} /><strong>外部副作用结果未知</strong><span>系统已停止自动重试。先核查外部系统，再选择结果；不确定时请放弃并创建新任务分支。</span></div>
    {receipts.map((receipt) => <div className="unknown-side-effect-row" key={receipt.id}>
      <div><strong>{receipt.tool_name}</strong><small>输入哈希 {receipt.input_hash.slice(0, 18)}… · 收据 {receipt.id.slice(0, 8)}</small></div>
      <textarea aria-label={`${receipt.tool_name} 副作用核查备注`} rows={2} placeholder="核查依据（放弃或确认未执行时必填）" value={notes[receipt.id] ?? ''} onChange={(event) => setNotes((current) => ({ ...current, [receipt.id]: event.target.value }))} />
      <div className="unknown-side-effect-actions"><button type="button" disabled={pending} onClick={() => onResolve(receipt.id, 'confirm_applied', notes[receipt.id] ?? '')}>确认已执行</button><button type="button" disabled={pending || !(notes[receipt.id] ?? '').trim()} onClick={() => onResolve(receipt.id, 'confirm_not_applied', notes[receipt.id] ?? '')}>确认未执行</button><button type="button" disabled={pending || !(notes[receipt.id] ?? '').trim()} onClick={() => onResolve(receipt.id, 'abandon', notes[receipt.id] ?? '')}>放弃并创建分支</button></div>
    </div>)}
    {error ? <small className="control-error">{error.message}</small> : null}
  </div>
}

function AgentTeamPanel({ room, loading, error }: { room?: AgentTeamRoom; loading: boolean; error: Error | null }) {
  const members = new Map(room?.members.map((member) => [member.id, member]) ?? [])
  const roleLabels = { coordinator: '协调者', researcher: '研究者', builder: '构建者', reviewer: '审查者' }
  const statusLabels = { planned: '已规划', working: '工作中', waiting: '等待依赖', done: '已完成', failed: '失败' }
  const messageLabels = { plan: '计划', handoff: '移交', result: '结果', review: '审查', synthesis: '最终合成' }
  return <section className="agent-team-panel" aria-label="Agent Team 协作室">
    <div className="panel-heading"><span><UsersRound size={18} />Agent Team 协作室</span><em>group cursor {room?.next_cursor ?? 0}</em></div>
    {error ? <div className="agent-team-error">{error.message}</div> : null}
    <div className="agent-team-layout">
      <aside className="agent-roster" aria-label="智能体状态">
        {room?.members.map((member) => <article key={member.id} className={`agent-member ${member.status}`}>
          <span className="agent-avatar">{member.display_name.slice(0, 1)}</span>
          <span><strong>{member.display_name}</strong><small>{roleLabels[member.role]} · {statusLabels[member.status]}</small><em>{member.model || '等待模型分配'}{member.current_step_id ? ` · ${member.current_step_id.slice(0, 8)}` : ''}</em></span>
        </article>)}
        {loading && !room ? <div className="agent-team-empty"><CircleDashed size={16} />正在组建团队…</div> : null}
      </aside>
      <div className="agent-group-chat" aria-label="多 Agent 群聊">
        {room?.messages.map((message) => {
          const sender = members.get(message.sender_agent_id)
          return <article className={`agent-message ${message.message_type}`} key={message.id}>
            <header><strong>{sender?.display_name ?? 'Agent'}</strong><span>{messageLabels[message.message_type]}</span><small>#{message.server_seq}</small></header>
            <p>{message.content}</p>
            <footer>{message.content_hash.slice(0, 20)}… · {new Date(message.created_at).toLocaleTimeString('zh-CN')}</footer>
          </article>
        })}
        {!loading && room?.messages.length === 0 ? <div className="agent-team-empty"><CircleDashed size={16} />协调者正在形成计划，消息提交后按服务端序号呈现。</div> : null}
      </div>
    </div>
  </section>
}

function ArtifactPublishRow({ artifact, projectId }: { artifact: Artifact; projectId: string }) {
  const [previewOpen, setPreviewOpen] = useState(false)
  const [visibility, setVisibility] = useState<Visibility>('private')
  const queryClient = useQueryClient()
  const save = useMutation({
    mutationFn: () => api.saveArtifactAsProjectFile(artifact.id, { name: artifact.name, visibility }),
    onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['assets', projectId] }),
  })
  return <div className="artifact-publish-row"><span><strong>{artifact.name}</strong><small>{artifact.kind === 'evidence' ? '研究过程材料' : '任务交付文件'} · {artifact.media_type} · {artifact.size_bytes} B</small></span><button type="button" className="artifact-open-link" onClick={() => setPreviewOpen(true)}><ExternalLink size={14} />直接打开</button><select aria-label={`${artifact.name} 保存范围`} value={visibility} disabled={save.isPending || save.isSuccess} onChange={(event) => setVisibility(event.target.value as Visibility)}><option value="private">仅自己</option><option value="project">项目成员</option></select><button type="button" title="保存后供项目后续任务复用，不会发布到项目外" disabled={save.isPending || save.isSuccess} onClick={() => save.mutate()}><FileUp size={14} />{save.isSuccess ? '已保存到项目资料' : save.isPending ? '保存中' : '保存到项目资料'}</button>{save.error ? <em>{save.error.message}</em> : null}
    <MarkdownPreviewDialog artifact={previewOpen ? artifact : null} open={previewOpen} onOpenChange={setPreviewOpen} />
  </div>
}

function EventRow({ event }: { event: RunEvent }) {
    const labels: Record<string, string> = {
    'run.created': 'Run 已创建', 'run.paused': '任务已暂停', 'run.resumed': '任务已继续', 'run.retried': '任务已从安全点重试', 'run.canceled': '任务已取消',
    'artifact.created': '验收产物已生成', 'outcome.accepted': '成果已由用户接受', 'step.started': 'Worker 开始执行',
    'step.recovered': 'Worker 已接管恢复', 'checkpoint.committed': '安全检查点已提交', 'step.succeeded': '步骤执行完成',
    'step.failed': '步骤执行失败', 'step.blocked': '步骤需要处理', 'tool.planned': '工具动作已完成策略判定', 'tool.execution_started': '工具执行收据已开启',
    'tool.completed': '工具执行收据已完成', 'tool.side_effect_unknown': '未知副作用已阻止重试', 'tool.side_effect_resolved': '未知副作用已由人工决策', 'approval.requested': '风险动作等待审批',
    'approval.decided': '审批决定已持久化', 'approval.expired': '审批已过期并默认拒绝', 'validation.completed': '确定性验收已完成',
    'advisor.previewed': 'Advisor 预览已生成', 'advisor.completed': 'Advisor 已批准', 'advisor.model_reviewed': 'Advisor 模型复核已记录',
    'agent_team.created': 'Agent Team 已组建', 'agent.message': 'Agent 消息已提交', 'agent.handoff': '子任务已移交',
    'agent_team.synthesis_queued': '依赖满足，已进入最终合成',
	'skill.invoked': '正在加载 Skill', 'skill.loaded': 'Skill 已加载', 'model.skill_resume_queued': 'Skill 已加载，继续执行',
	'execution.plan_created': '执行计划已生成', 'model.message': '模型已提交本轮进展', 'model.continuation_queued': '工具结果已回传，继续执行', 'model.retry_queued': '模型交互异常，已自动重试', 'tool.recovery_queued': '工具失败，已进入自动修复',
  }
  const label = labels[event.event_type] ?? event.event_type
  const contextPlan = event.payload.context_plan as { estimated_tokens_saved?: number; skills?: { loaded?: number }; tool_definitions_after?: number } | undefined
  const contextDetail = contextPlan ? `上下文计划${contextPlan.estimated_tokens_saved ? ` · 预计节省 ${contextPlan.estimated_tokens_saved} Token` : ''}${typeof contextPlan.tool_definitions_after === 'number' ? ` · 工具 ${contextPlan.tool_definitions_after} 个` : ''}${contextPlan.skills && typeof contextPlan.skills.loaded === 'number' ? ` · Skill ${contextPlan.skills.loaded} 个` : ''}` : ''
	const skillNames = Array.isArray(event.payload.skill_names) ? event.payload.skill_names.filter((item): item is string => typeof item === 'string') : []
	const skillName = typeof event.payload.skill_name === 'string' ? event.payload.skill_name : skillNames.join('、')
	const allowedTools = Array.isArray(event.payload.allowed_tools) ? event.payload.allowed_tools.filter((item): item is string => typeof item === 'string') : []
	const skillDetail = skillName ? `${skillName}${allowedTools.length > 0 ? ` · 允许 ${allowedTools.join('、')}` : ''}` : ''
  return <div className="event-row"><span className={`event-glyph ${event.event_type.split('.').at(-1)}`}><CheckCircle2 size={15} /></span><span><strong>{label}</strong><small>{new Date(event.occurred_at).toLocaleTimeString('zh-CN')} · #{event.stream_seq}{skillDetail ? ` · ${skillDetail}` : ''}{contextDetail ? ` · ${contextDetail}` : ''}</small></span></div>
}

function formatDuration(start: string, end: string) {
  const milliseconds = Math.max(0, new Date(end).getTime() - new Date(start).getTime())
  const seconds = Math.round(milliseconds / 1000)
  if (seconds < 60) return `${seconds} 秒`
  const minutes = Math.floor(seconds / 60)
  const remainder = seconds % 60
  return remainder ? `${minutes} 分 ${remainder} 秒` : `${minutes} 分钟`
}
