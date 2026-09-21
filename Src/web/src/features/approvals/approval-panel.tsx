import { useMemo, useState, type KeyboardEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, Check, ChevronLeft, ChevronRight, ShieldAlert, X } from 'lucide-react'
import { api, type HumanAction } from '../../shared/api/client'
import { HumanActionCard } from './human-action-card'
import { approvalBatchable, approvalPending, reconcileApprovals } from './approval-queue'

export function ApprovalPanel({ actions }: { actions: HumanAction[] }) {
  const queryClient = useQueryClient()
  const [reviewed, setReviewed] = useState<HumanAction[]>(() => actions.filter(approvalPending))
  const [selectedId, setSelectedId] = useState<string>()
  const [busy, setBusy] = useState(false)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [batchConfirm, setBatchConfirm] = useState(false)
  const items = useMemo(() => reconcileApprovals(reviewed, actions), [reviewed, actions])
  const selected = items.find((item) => item.id === selectedId) ?? items.find(approvalPending) ?? items[items.length - 1]
  const index = selected ? items.findIndex((item) => item.id === selected.id) : 0
  const remaining = items.filter(approvalPending)
  const eligible = items.filter(approvalBatchable)
  const move = (delta: number) => {
    if (busy) return
    const next = items[index + delta]
    if (next) { setSelectedId(next.id); setBatchConfirm(false) }
  }
  const decided = (result: HumanAction) => {
    setReviewed((previous) => reconcileApprovals(previous, previous.map((item) => item.id === result.id ? result : item)))
    setErrors((previous) => { const next = { ...previous }; delete next[result.id]; return next })
    setSelectedId(items.slice(index + 1).find((item) => item.id !== result.id && approvalPending(item))?.id ?? items.find((item) => item.id !== result.id && approvalPending(item))?.id)
  }
  const approveAll = async () => {
    if (busy) return
    const batch = [...eligible]
    setBusy(true)
    setBatchConfirm(false)
    const results = await Promise.allSettled(batch.map((action) => api.decideHumanAction(action.id, { decision: 'approved', scope: 'once', version: action.version })))
    const updates = new Map<string, HumanAction>()
    const failed: Record<string, string> = {}
    results.forEach((result, position) => {
      const id = batch[position]!.id
      if (result.status === 'fulfilled') updates.set(id, result.value)
      else failed[id] = result.reason instanceof Error ? result.reason.message : '审批未完成，请刷新后重试'
    })
    setReviewed((previous) => reconcileApprovals(previous, previous.map((item) => updates.get(item.id) ?? item)))
    setErrors(failed)
    setSelectedId(Object.keys(failed)[0] ?? items.find((item) => approvalPending(item) && !updates.has(item.id))?.id)
    try {
      await Promise.all(['human-actions', 'approvals', 'run-plan', 'run-events', 'tasks', 'inbox'].map((key) => queryClient.invalidateQueries({ queryKey: [key] })))
    } finally { setBusy(false) }
  }
  const onKeyDown = (event: KeyboardEvent<HTMLElement>) => {
    if (event.target instanceof HTMLElement && event.target.closest('input, textarea, select, [contenteditable="true"], [role="dialog"]')) return
    if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') { event.preventDefault(); move(event.key === 'ArrowLeft' ? -1 : 1) }
    if (event.key === 'Escape') setBatchConfirm(false)
    // Enter uses the focused native button; never approve globally from a field.
  }
  if (!selected) return null
  return <section className="human-action-panel" aria-label="审批面板" onKeyDown={onKeyDown}>
    <header className="human-action-panel-head">
      <div><span className="human-action-panel-eyebrow"><ShieldAlert size={13} />{remaining.length ? '需要你处理' : '本轮审批已处理'}</span><strong>{remaining.length ? `待处理 ${remaining.length} 项` : '已处理全部审批'}</strong></div>
      <div className="human-action-panel-nav"><button type="button" disabled={busy || index === 0} aria-label="上一项审批" onClick={() => move(-1)}><ChevronLeft size={17} /></button><span aria-live="polite">{index + 1} / {items.length}</span><button type="button" disabled={busy || index === items.length - 1} aria-label="下一项审批" onClick={() => move(1)}><ChevronRight size={17} /></button></div>
    </header>
    <nav className="human-action-progress" aria-label="审批项目导航">{items.map((item, position) => <button key={item.id} type="button" disabled={busy} className={`${item.id === selected.id ? 'active' : ''} ${errors[item.id] ? 'error' : item.status}`} aria-current={item.id === selected.id ? 'step' : undefined} aria-label={`第 ${position + 1} 项：${item.title}，${errors[item.id] ? '处理失败' : approvalPending(item) ? '待处理' : item.status === 'approved' ? '已批准' : '已结束'}`} title={`${item.title} · ${item.risk_level}`} onClick={() => { setSelectedId(item.id); setBatchConfirm(false) }}>{errors[item.id] ? <AlertTriangle size={13} /> : item.status === 'approved' ? <Check size={13} /> : !approvalPending(item) ? <X size={13} /> : position + 1}</button>)}</nav>
    <div className="human-action-panel-body" aria-busy={busy}>
      <HumanActionCard key={selected.id} action={selected} onDecided={decided} onBusy={setBusy} onError={(message) => setErrors((previous) => ({ ...previous, [selected.id]: message }))} disabled={busy} />
      {errors[selected.id] ? <p className="human-action-error" role="alert">{errors[selected.id]}</p> : null}
    </div>
    <footer className="human-action-panel-foot">
      <span>{remaining.length ? '每项保留独立参数与审批记录' : '决定已保存，可切换查看处理结果'}</span>
      {eligible.length > 1 ? <button type="button" className="human-action-approve-all" disabled={busy} onClick={() => setBatchConfirm((value) => !value)}><Check size={14} />全部批准一次 · {eligible.length} 项</button> : null}
      {remaining.length > eligible.length && remaining.length > 1 ? <small>专项授权仍需逐项处理</small> : null}
      {batchConfirm ? <div className="approval-batch-confirm"><p>批准当前已列出的 {eligible.length} 项各一次；不授权后续新调用。</p><button type="button" onClick={() => setBatchConfirm(false)}>返回逐项审批</button><button type="button" className="approve" onClick={() => void approveAll()}>确认批准 {eligible.length} 项</button></div> : null}
    </footer>
  </section>
}
