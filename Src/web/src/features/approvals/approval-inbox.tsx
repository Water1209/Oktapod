import { useState } from 'react'
import * as Dialog from '@radix-ui/react-dialog'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Check, Clock3, ExternalLink, LockKeyhole, ShieldAlert, X } from 'lucide-react'
import { api, type Approval, type ApprovalScope, type InboxHistoryItem, type InboxItem } from '../../shared/api/client'
import { backgroundInboxRefetchInterval } from '../../shared/query-refresh'
import { RunBudgetControl } from '../tasks/run-budget-control'
import { taskFailureInfo } from '../tasks/task-progress-model'

export function ApprovalInbox() {
  const [view, setView] = useState<'pending' | 'risk' | 'history'>('pending')
  const [riskStatus, setRiskStatus] = useState<'all' | 'approved' | 'rejected'>('all')
  const [selected, setSelected] = useState<string[]>([])
  const queryClient = useQueryClient()
  const approvalStatus = view === 'pending' ? 'pending' : riskStatus
  const approvals = useQuery({ queryKey: ['approvals', approvalStatus], queryFn: () => api.approvals(approvalStatus), enabled: view !== 'history' })
  const inbox = useQuery({ queryKey: ['inbox'], queryFn: api.inbox, refetchInterval: (query) => backgroundInboxRefetchInterval(query.state.data) })
  const batch = useMutation({
    mutationFn: async (decision: 'approved' | 'rejected') => {
      const ids = [...selected]
      if (ids.length === 0) throw new Error('请先选择待处理审批')
      if (decision === 'approved') {
        const selectedApprovals = (approvals.data?.items ?? []).filter((item) => ids.includes(item.id))
        const signature = selectedApprovals[0]
        const sameSignature = signature && selectedApprovals.every((item) =>
          item.tool_name === signature.tool_name
          && item.risk_level === signature.risk_level
          && item.input_hash === signature.input_hash,
        )
        if (!sameSignature) throw new Error('批量批准仅支持相同工具、相同冻结参数和相同风险；不同动作请逐项处理。')
      }
      return Promise.all(ids.map((id) => api.decideApproval(id, decision)))
    },
    onSuccess: async () => {
      setSelected([])
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['approvals'] }),
        queryClient.invalidateQueries({ queryKey: ['inbox'] }),
      ])
    },
  })
  const pendingIDs = (approvals.data?.items ?? []).filter((item) => item.status === 'pending' && item.tool_name !== 'web_fetch').map((item) => item.id)
  const allPendingSelected = pendingIDs.length > 0 && pendingIDs.every((id) => selected.includes(id))
  const toggleSelected = (id: string) => setSelected((current) => current.includes(id) ? current.filter((value) => value !== id) : [...current, id])
  const toggleAll = () => setSelected(allPendingSelected ? [] : pendingIDs)
  const actionItems = inbox.data?.items.filter((item) => item.kind !== 'approval') ?? []
  const historyItems = inbox.data?.history ?? []

  return <div className="page approval-page">
    <section className="page-intro compact-intro">
      <div><p className="eyebrow">治理</p><h1>审批中心</h1><p>待办、风险授权和处理历史各自归位；执行凭证仍保留在任务的安全与追踪中。</p></div>
      <div className="approval-filter" aria-label="审批中心视图">
        <button className={view === 'pending' ? 'active' : ''} onClick={() => { setView('pending'); setSelected([]) }}>待我处理</button>
        <button className={view === 'risk' ? 'active' : ''} onClick={() => { setView('risk'); setSelected([]) }}>风险审批</button>
        <button className={view === 'history' ? 'active' : ''} onClick={() => { setView('history'); setSelected([]) }}>处理记录</button>
      </div>
    </section>

    {view !== 'history' && pendingIDs.length > 0 ? <section className="approval-batch-toolbar" aria-label="批量处理审批"><label><input type="checkbox" aria-label="全选待处理审批" checked={allPendingSelected} onChange={toggleAll} />全选待处理审批</label><span>已选 {selected.length} 项</span><button type="button" className="reject" disabled={selected.length === 0 || batch.isPending} onClick={() => batch.mutate('rejected')}>批量拒绝</button><button type="button" className="approve" disabled={selected.length === 0 || batch.isPending} onClick={() => batch.mutate('approved')}>批量批准一次</button>{batch.error ? <small className="approval-error">{batch.error.message}</small> : null}</section> : null}

    <section className="approval-list">
      {view === 'pending' ? <div className="approval-list-section"><header><h2>待我处理</h2><span>{actionItems.length + (approvals.data?.items.length ?? 0)} 项</span></header>
        {actionItems.map((item) => <InboxSummary item={item} key={`${item.kind}:${item.id}`} />)}
        {approvals.data?.items.map((approval) => <ApprovalCard approval={approval} key={approval.id} selectable={approval.tool_name !== 'web_fetch'} selected={selected.includes(approval.id)} onSelect={() => toggleSelected(approval.id)} />)}
        {!approvals.isLoading && actionItems.length === 0 && approvals.data?.items.length === 0 ? <div className="empty-panel"><Check size={28} /><h2>当前没有待处理事项</h2><p>需要补充输入、恢复任务或人工授权的事项会出现在这里。</p></div> : null}
      </div> : null}
      {view === 'risk' ? <div className="approval-list-section"><header><h2>风险审批</h2><div className="approval-filter compact" aria-label="风险审批状态">{(['all', 'approved', 'rejected'] as const).map((item) => <button className={riskStatus === item ? 'active' : ''} key={item} onClick={() => setRiskStatus(item)}>{item === 'all' ? '全部审批' : statusLabel(item)}</button>)}</div><span>{approvals.data?.items.length ?? 0} 项</span></header>
        {approvals.isLoading ? <div className="page-loading">正在恢复审批队列…</div> : null}
        {approvals.error ? <div className="empty-panel"><h2>审批队列无法读取</h2><p>{approvals.error.message}</p></div> : null}
        {approvals.data?.items.map((approval) => <ApprovalCard approval={approval} key={approval.id} selectable={approval.status === 'pending' && approval.tool_name !== 'web_fetch'} selected={selected.includes(approval.id)} onSelect={() => toggleSelected(approval.id)} />)}
        {approvals.data?.items.length === 0 ? <div className="empty-panel"><ShieldAlert size={28} /><h2>当前没有风险审批</h2><p>最近没有触发人工授权，或动作已按“完全访问 / 自动审查”处理。执行 Receipt 仍可在任务的“安全”和“追踪”中查看。</p></div> : null}
      </div> : null}
      {view === 'history' ? <div className="approval-list-section approval-history-list"><header><h2>处理记录</h2><span>{historyItems.length} 项</span></header>{historyItems.map((item) => <InboxHistory item={item} key={`history:${item.id}`} />)}{!inbox.isLoading && historyItems.length === 0 ? <div className="empty-panel"><Clock3 size={28} /><h2>还没有处理记录</h2><p>任务验收、暂停、恢复、取消、审批决定和副作用核查会记录在这里。</p></div> : null}</div> : null}
    </section>
  </div>
}

function InboxHistory({ item }: { item: InboxHistoryItem }) {
  return <article className="approval-card compact inbox-summary inbox-history">
    <div className="approval-card-head"><span className="risk-chip l1">历史</span><span><strong>{item.title}</strong><small><Clock3 size={12} />{new Date(item.occurred_at).toLocaleString('zh-CN')}</small></span><span className="approval-state">{item.kind === 'approval' ? '审批' : item.kind === 'side_effect' ? '副作用' : '任务'}</span></div>
    <p className="approval-reason">{item.summary}</p>
    <div className="approval-actions"><Link to="/tasks/$taskId" params={{ taskId: item.task_id }}>打开任务<ExternalLink size={13} /></Link></div>
  </article>
}

function InboxSummary({ item }: { item: InboxItem }) {
  const task = useQuery({ queryKey: ['task', item.task_id], queryFn: () => api.task(item.task_id), enabled: item.kind === 'task_recovery' })
  const usage = useQuery({ queryKey: ['run-usage', task.data?.run_id], queryFn: () => api.runUsage(task.data!.run_id), enabled: !!task.data?.run_id && task.data.error_code === 'BUDGET_RESERVATION_EXCEEDED' })
  const failure = task.data ? taskFailureInfo(task.data) : undefined
  const budgetIssue = task.data?.error_code === 'BUDGET_RESERVATION_EXCEEDED'
  const label = item.kind === 'task_input' ? '等待补充输入' : item.kind === 'side_effect_unknown' ? '未知副作用待核查' : budgetIssue ? '配额不足，等待批准' : task.data?.status === 'failed' ? '任务已停止' : '等待决定是否恢复'
  const reason = failure?.detail ?? (item.reason === 'BUDGET_RESERVATION_EXCEEDED' ? '本次运行的 Token 或模型调用配额不足，系统已保留执行进度。' : item.reason)
  return <article className="approval-card compact inbox-summary">
    <div className="approval-card-head"><span className="risk-chip l1">待办</span><span><strong>{item.title}</strong><small><Clock3 size={12} />{new Date(item.updated_at).toLocaleString('zh-CN')}</small></span><span className="approval-state">{label}</span></div>
    <p className="approval-reason">{reason}</p>
    {budgetIssue && task.data && usage.data ? <RunBudgetControl taskId={task.data.id} runId={task.data.run_id} taskStatus={task.data.status} usage={usage.data} compact /> : null}
    {budgetIssue && usage.error ? <small className="approval-error">配额信息读取失败：{usage.error.message}</small> : null}
    <div className="approval-actions"><Link to="/tasks/$taskId" params={{ taskId: item.task_id }}>打开任务<ExternalLink size={13} /></Link></div>
  </article>
}

export function ApprovalCard({ approval, compact = false, selectable = false, selected = false, onSelect, onDecided }: { approval: Approval; compact?: boolean; selectable?: boolean; selected?: boolean; onSelect?: () => void; onDecided?: (approval: Approval) => void }) {
  const queryClient = useQueryClient()
  const [projectConfirmOpen, setProjectConfirmOpen] = useState(false)
  const decision = useMutation({
    mutationFn: ({ decision, scope, confirmed = false }: { decision: 'approved' | 'rejected'; scope: ApprovalScope; confirmed?: boolean }) => api.decideApproval(approval.id, decision, '', scope, confirmed),
    onSuccess: (updated) => {
      setProjectConfirmOpen(false)
      void queryClient.invalidateQueries({ queryKey: ['approvals'] })
      void queryClient.invalidateQueries({ queryKey: ['task', updated.task_id] })
      void queryClient.invalidateQueries({ queryKey: ['run-events', updated.run_id] })
      void queryClient.invalidateQueries({ queryKey: ['conversation-messages'] })
      void queryClient.invalidateQueries({ queryKey: ['tasks', updated.project_id] })
      onDecided?.(updated)
    },
  })
  const params = formatFrozenParams(approval)
  const privateWebFetch = approval.tool_name === 'web_fetch' && approval.approval_scope === 'run_origin'
	const persistentApprovalSupported = !approval.tool_name.startsWith('mcp__') && !privateWebFetch
  return <article className={`approval-card ${compact ? 'compact' : ''}`}>
    <div className="approval-card-head">
      {selectable ? <input type="checkbox" aria-label={`选择审批 ${approval.tool_name}`} checked={selected} onChange={onSelect} /> : null}
      <span className={`risk-chip ${approval.risk_level.toLowerCase()}`}>{approval.risk_level}</span>
      <span><strong>{toolLabel(approval.tool_name)}</strong><small><Clock3 size={12} />{new Date(approval.requested_at).toLocaleString('zh-CN')} · 截止 {new Date(approval.expires_at).toLocaleString('zh-CN')}</small></span>
      <span className={`approval-state ${approval.status}`}>{statusLabel(approval.status)}</span>
    </div>
    <p className="approval-reason">{approval.reason}</p>
	{approval.status === 'pending' && approval.tool_name.startsWith('mcp__') ? <small className="approval-error">外部 MCP 仅支持逐次批准，避免把一次确认扩展为后续未知参数。</small> : null}
    {privateWebFetch ? <div className="private-origin-approval"><ShieldAlert size={16} /><span><strong>受控私网读取</strong><small>只读 GET · 当前 Run · 最长 24 小时</small><code>{approval.target_origin}</code>{approval.matched_rule_id ? <small>匹配规则：{approval.matched_rule_id}</small> : null}<p>批准后，模型可读取此 origin 的其他路径，并可能把页面数据带入模型上下文；不会授权其他端口、协议或 Run。</p></span></div> : null}
    <div className="frozen-params"><LockKeyhole size={15} /><span><small>冻结参数</small><code>{params}</code></span></div>
    <div className="approval-hash"><span>INPUT HASH</span><code>{approval.input_hash}</code></div>
    <div className="approval-actions">
      <Link to="/tasks/$taskId" params={{ taskId: approval.task_id }}>打开任务<ExternalLink size={13} /></Link>
      {approval.status === 'pending' ? <>
        <button className="reject" disabled={decision.isPending} onClick={() => decision.mutate({ decision: 'rejected', scope: 'once' })}><X size={14} />拒绝</button>
		{privateWebFetch ? <button className="approve approve-origin" disabled={decision.isPending} onClick={() => decision.mutate({ decision: 'approved', scope: 'run_origin' })}><Check size={14} />允许本任务访问此站点</button> : <button className="approve" disabled={decision.isPending} onClick={() => decision.mutate({ decision: 'approved', scope: 'once' })}><Check size={14} />批准一次</button>}
		{persistentApprovalSupported ? <button className="approve approve-session" disabled={decision.isPending} onClick={() => decision.mutate({ decision: 'approved', scope: 'conversation' })}><Check size={14} />本会话允许同类动作</button> : null}
		{persistentApprovalSupported ? <button className="approve approve-project" disabled={decision.isPending} onClick={() => setProjectConfirmOpen(true)}><Check size={14} />本项目允许 30 天</button> : null}
      </> : null}
    </div>
    {decision.error ? <small className="approval-error">{decision.error.message}</small> : null}
    <Dialog.Root open={projectConfirmOpen} onOpenChange={setProjectConfirmOpen}>
      <Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><Dialog.Content className="dialog-content human-action-dialog" aria-describedby={`legacy-approval-description-${approval.id}`}><Dialog.Title>允许当前项目使用同类动作？</Dialog.Title><Dialog.Description id={`legacy-approval-description-${approval.id}`}>授权有效期为 30 天，只匹配当前工具、精确参数形状和风险等级。静态黑名单不可绕过。</Dialog.Description><div className="dialog-actions"><Dialog.Close asChild><button type="button">取消</button></Dialog.Close><button className="primary" type="button" disabled={decision.isPending} onClick={() => decision.mutate({ decision: 'approved', scope: 'project', confirmed: true })}>确认授权</button></div></Dialog.Content></Dialog.Portal>
    </Dialog.Root>
  </article>
}

function formatFrozenParams(approval: Approval) {
  if (approval.tool_name === 'web_fetch') return String(approval.frozen_params.url ?? 'invalid url')
  if (approval.tool_name === 'workspace_run_command') {
    const program = typeof approval.frozen_params.program === 'string' ? approval.frozen_params.program : 'invalid'
    const args = Array.isArray(approval.frozen_params.args) ? approval.frozen_params.args.map(String) : []
    return [program, ...args].map((item) => JSON.stringify(item)).join(' ')
  }
  if (approval.tool_name === 'workspace_write_file') {
    return String(approval.frozen_params.path ?? 'invalid path')
  }
  return JSON.stringify(approval.frozen_params)
}

function toolLabel(tool: string) {
  if (tool === 'workspace_run_command') return '运行工作区命令'
  if (tool === 'workspace_write_file') return '写入工作区文件'
  if (tool === 'web_fetch') return '读取网页'
  return tool
}

function statusLabel(status: string) {
  if (status === 'pending') return '待处理'
  if (status === 'approved') return '已批准'
  if (status === 'rejected') return '已拒绝'
  return '全部'
}
