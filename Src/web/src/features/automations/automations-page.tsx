import { useMemo, useState } from 'react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import * as Dialog from '@radix-ui/react-dialog'
import { Clock3, Ellipsis, Folder, Plus, Search, Sparkles, X } from 'lucide-react'
import { api, type Schedule, type ScheduleFireStatus, type ScheduleMisfirePolicy, type ScheduleOverlapPolicy } from '../../shared/api/client'

type Project = Awaited<ReturnType<typeof api.projects>>['items'][number]

export function AutomationsPage() {
  const queryClient = useQueryClient()
  const projects = useQuery({ queryKey: ['projects'], queryFn: api.projects })
  const projectItems = useMemo(() => projects.data?.items ?? [], [projects.data?.items])
  const scheduleQueries = useQueries({ queries: projectItems.map((project) => ({ queryKey: ['schedules', project.id], queryFn: () => api.schedules(project.id), refetchInterval: 15_000 })) })
  const schedules = useMemo(() => scheduleQueries.flatMap((query, index) => (query.data?.items ?? []).map((schedule) => ({ schedule, project: projectItems[index] }))), [projectItems, scheduleQueries])
  const [search, setSearch] = useState('')
  const [filter, setFilter] = useState<'all' | 'enabled' | 'disabled'>('all')
  const [dialogOpen, setDialogOpen] = useState(false)
  const [selectedProjectId, setSelectedProjectId] = useState('')
  const [selectedSchedule, setSelectedSchedule] = useState<string>()
  const filtered = schedules.filter(({ schedule, project }) => {
    const text = `${schedule.name} ${schedule.goal} ${project?.name ?? ''}`.toLowerCase()
    return (filter === 'all' || schedule.status === filter) && (!search.trim() || text.includes(search.trim().toLowerCase()))
  })
  const toggle = useMutation({ mutationFn: ({ id, status }: { id: string; status: 'enabled' | 'disabled' }) => api.setScheduleStatus(id, status), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['schedules'] }) })
  const openCreate = () => { setSelectedProjectId(selectedProjectId || projectItems[0]?.id || ''); setDialogOpen(true) }

  if (projects.isLoading) return <div className="page-shell"><div className="loading-state">正在加载定时任务…</div></div>
  if (!projectItems.length) return <div className="page-shell"><div className="empty-panel"><Clock3 size={25} /><h1>定时任务</h1><p>创建项目后，可以在这里统一管理所有定时任务。</p></div></div>

  return <div className="page-shell automation-page">
    <header className="automation-page-header"><div><p className="eyebrow">AUTOMATIONS</p><h1>定时任务</h1><p>让项目按时执行工作。每次触发都会创建一个新的普通任务，并沿用项目的权限和审计规则。</p></div><button className="primary-button automation-create-button" type="button" onClick={openCreate}><Plus size={16} />新建</button></header>
    <div className="automation-toolbar"><label className="automation-search"><Search size={16} /><span className="sr-only">搜索定时任务</span><input aria-label="搜索定时任务" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索定时任务或项目" /></label><div className="automation-filters" role="tablist" aria-label="定时任务状态"><button type="button" className={filter === 'all' ? 'active' : ''} onClick={() => setFilter('all')}>全部 <span>{schedules.length}</span></button><button type="button" className={filter === 'enabled' ? 'active' : ''} onClick={() => setFilter('enabled')}>已启用 <span>{schedules.filter(({ schedule }) => schedule.status === 'enabled').length}</span></button><button type="button" className={filter === 'disabled' ? 'active' : ''} onClick={() => setFilter('disabled')}>已停用 <span>{schedules.filter(({ schedule }) => schedule.status === 'disabled').length}</span></button></div></div>
    <section className="automation-list" aria-label="定时任务列表">{filtered.map(({ schedule, project }) => <AutomationRow key={schedule.id} schedule={schedule} project={project} expanded={selectedSchedule === schedule.id} onExpand={() => setSelectedSchedule(selectedSchedule === schedule.id ? undefined : schedule.id)} onToggle={() => toggle.mutate({ id: schedule.id, status: schedule.status === 'enabled' ? 'disabled' : 'enabled' })} togglePending={toggle.isPending} />)}{filtered.length === 0 ? <div className="automation-empty"><Clock3 size={22} /><strong>{search ? '没有匹配的定时任务' : '还没有定时任务'}</strong><span>{search ? '换一个关键词试试。' : '创建第一个计划，让项目自动开始工作。'}</span>{!search ? <button className="secondary-button" type="button" onClick={openCreate}><Plus size={15} />新建定时任务</button> : null}</div> : null}</section>
    <CreateScheduleDialog open={dialogOpen} onOpenChange={setDialogOpen} projects={projectItems} selectedProjectId={selectedProjectId} onProjectChange={setSelectedProjectId} />
  </div>
}

function AutomationRow({ schedule, project, expanded, onExpand, onToggle, togglePending }: { schedule: Schedule; project?: Project; expanded: boolean; onExpand: () => void; onToggle: () => void; togglePending: boolean }) {
  const runs = useQuery({ queryKey: ['schedule-runs', schedule.id], queryFn: () => api.scheduleRuns(schedule.id), enabled: expanded, refetchInterval: expanded && schedule.status === 'enabled' ? 5_000 : false })
  return <article className={`automation-row${expanded ? ' expanded' : ''}`}><div className="automation-row-main"><div className={`automation-status-dot ${schedule.status}`} aria-label={schedule.status === 'enabled' ? '已启用' : '已停用'} /><div className="automation-row-copy"><div className="automation-row-title"><strong>{schedule.name}</strong><span className="automation-project-tag"><Folder size={12} />{project?.name ?? '未知项目'}</span></div><p>{schedule.goal}</p><small>{schedule.cron_expression} · {schedule.timezone} · 下次 {new Date(schedule.next_run_at).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' })}</small></div></div><div className="automation-row-actions"><button className="secondary-button" type="button" onClick={onExpand}>{expanded ? '收起' : '执行记录'}</button><button className="secondary-button" type="button" onClick={onToggle} disabled={togglePending}>{schedule.status === 'enabled' ? '停用' : '启用'}</button><button className="icon-button" type="button" aria-label="更多操作" title="更多操作"><Ellipsis size={17} /></button></div>{expanded ? <div className="automation-run-history">{runs.isLoading ? '正在加载执行记录…' : runs.data?.items.length ? runs.data.items.slice(0, 8).map((run) => <div className="automation-run-item" key={run.id}><span>{new Date(run.planned_at).toLocaleString('zh-CN')}</span><strong>{scheduleRunStatus(run.status, run.task_status)}</strong>{run.error_code ? <em>{run.error_code}</em> : null}</div>) : '暂无执行记录'}</div> : null}</article>
}

const fireStatusLabel: Record<ScheduleFireStatus, string> = {
  pending: '触发处理中',
  created: '已创建任务',
  skipped: '已跳过',
  waiting_confirmation: '等待确认',
}

const taskStatusLabel: Record<string, string> = {
  queued: '任务排队中', running: '任务执行中', waiting_approval: '任务等待审批', waiting_input: '任务等待输入',
  paused: '任务已暂停', blocked: '任务受阻', succeeded: '任务已完成', failed: '任务失败', canceled: '任务已取消', unknown: '任务状态未知',
}

function scheduleRunStatus(fireStatus: ScheduleFireStatus, taskStatus: string) {
  const fire = fireStatusLabel[fireStatus] ?? fireStatus
  if (!taskStatus) return fire
  return `${fire} · ${taskStatusLabel[taskStatus] ?? taskStatus}`
}

function CreateScheduleDialog({ open, onOpenChange, projects, selectedProjectId, onProjectChange }: { open: boolean; onOpenChange: (open: boolean) => void; projects: Project[]; selectedProjectId: string; onProjectChange: (id: string) => void }) {
  const queryClient = useQueryClient()
  const [mode, setMode] = useState<'natural' | 'manual'>('natural')
  const [natural, setNatural] = useState('')
  const [name, setName] = useState('')
  const [cron, setCron] = useState('0 9 * * *')
  const [timezone, setTimezone] = useState(() => Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC')
  const [goal, setGoal] = useState('')
  const [misfire, setMisfire] = useState<ScheduleMisfirePolicy>('ask')
  const [overlap, setOverlap] = useState<ScheduleOverlapPolicy>('forbid')
   const create = useMutation({ mutationFn: () => api.createSchedule(selectedProjectId, { name: name.trim(), cron_expression: cron.trim(), timezone: timezone.trim(), goal: goal.trim(), mode: 'single_agent', visibility: 'private', misfire_policy: misfire, overlap_policy: overlap, misfire_grace_seconds: 300 }), onSuccess: async () => { await queryClient.invalidateQueries({ queryKey: ['schedules'] }); setNatural(''); setName(''); setGoal(''); onOpenChange(false) } })
  const preview = useMutation({ mutationFn: () => api.previewSchedule(selectedProjectId, natural, timezone), onSuccess: (draft) => { setName(draft.name); setCron(draft.cron_expression); setTimezone(draft.timezone || timezone); setGoal(draft.goal); setMode('manual') } })
  const parseNatural = () => { const parts = natural.replace(/^创建计划\s*[:：]\s*/i, '').split('|').map((part) => part.trim()); if (parts.length === 4) { setName(parts[0] ?? ''); setCron(parts[1] ?? ''); setTimezone(parts[2] ?? ''); setGoal(parts[3] ?? ''); setMode('manual') } }
   return <Dialog.Root open={open} onOpenChange={onOpenChange}><Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><Dialog.Content className="dialog-content automation-dialog"><div className="dialog-heading"><div><Dialog.Title>新建定时任务</Dialog.Title><Dialog.Description>选择任务所属项目，再设置执行方式。创建后可以随时停用。</Dialog.Description></div><Dialog.Close className="icon-button" aria-label="关闭"><X size={17} /></Dialog.Close></div><label className="automation-project-select"><span>所属项目</span><select aria-label="定时任务所属项目" value={selectedProjectId} onChange={(event) => onProjectChange(event.target.value)}>{projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</select></label><div className="automation-mode-tabs" role="tablist"><button type="button" className={mode === 'natural' ? 'active' : ''} onClick={() => setMode('natural')}><Sparkles size={15} />自然语言</button><button type="button" className={mode === 'manual' ? 'active' : ''} onClick={() => setMode('manual')}>手动设置</button></div>{mode === 'natural' ? <div className="natural-schedule-panel"><textarea aria-label="自然语言描述" value={natural} onChange={(event) => setNatural(event.target.value)} placeholder="例如：每个工作日上午 9 点，整理项目最近的进展并生成简报" /><p>模型会把描述转换为计划草案；确认 Cron、时区和任务目标后，才会创建定时任务。</p><button className="primary-button" type="button" onClick={() => preview.mutate()} disabled={!natural.trim() || preview.isPending}>{preview.isPending ? "正在解析…" : "让模型解析"}</button><button className="secondary-button" type="button" onClick={parseNatural} disabled={!natural.trim()}>使用固定格式解析</button>{preview.error ? <div className="form-error">{preview.error.message}</div> : null}<div className="form-hint">解析结果会先填入手动设置；确认名称、Cron、时区和任务目标后才会创建。</div></div> : <form className="automation-form" onSubmit={(event) => { event.preventDefault(); if (name.trim() && cron.trim() && timezone.trim() && goal.trim()) create.mutate() }}><div className="automation-form-grid"><label><span>名称</span><input aria-label="计划名称" value={name} onChange={(event) => setName(event.target.value)} placeholder="例如：每日项目简报" required /></label><label><span>Cron</span><input aria-label="Cron 表达式" value={cron} onChange={(event) => setCron(event.target.value)} placeholder="0 9 * * *" required /></label><label><span>时区</span><input aria-label="时区" value={timezone} onChange={(event) => setTimezone(event.target.value)} placeholder="Asia/Shanghai" required /></label><label className="automation-form-wide"><span>任务目标</span><textarea aria-label="计划目标" value={goal} onChange={(event) => setGoal(event.target.value)} placeholder="触发后要完成什么？" required /></label><label><span>错过触发</span><select aria-label="错过触发策略" value={misfire} onChange={(event) => setMisfire(event.target.value as ScheduleMisfirePolicy)}><option value="ask">待人工确认</option><option value="run_now">立即补跑</option><option value="skip">跳过</option></select></label><label><span>重叠执行</span><select aria-label="重叠执行策略" value={overlap} onChange={(event) => setOverlap(event.target.value as ScheduleOverlapPolicy)}><option value="forbid">禁止并发</option><option value="queue">排队</option><option value="replace">替换旧任务</option></select></label></div><div className="dialog-actions"><Dialog.Close asChild><button className="secondary-button" type="button">取消</button></Dialog.Close><button className="primary-button" type="submit" disabled={create.isPending || !name.trim() || !goal.trim()}>创建定时任务</button></div>{create.error ? <div className="form-error">{create.error.message}</div> : null}</form>}</Dialog.Content></Dialog.Portal></Dialog.Root>
}
