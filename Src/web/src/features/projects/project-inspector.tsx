import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, ChevronRight, Clock3, ExternalLink, File as FileIcon, FileUp, LockKeyhole, Workflow, XCircle } from 'lucide-react'
import { api, type AgentTeamRoom, type Approval, type ApprovalRule, type Artifact, type RunEvent, type RunUsage, type Task, type Visibility } from '../../shared/api/client'

const statusLabels: Record<string, string> = {
  queued: '待开始', running: '运行中', waiting_approval: '等待审批', waiting_input: '等待输入', paused: '已暂停', blocked: '执行受阻', succeeded: '已完成', failed: '失败', canceled: '已取消',
}

export function InspectorRun({ task, events, usage, artifacts, agentTeam }: { task?: Task; events: RunEvent[]; usage?: RunUsage; artifacts: Artifact[]; agentTeam?: AgentTeamRoom }) {
  const queryClient = useQueryClient()
  const accept = useMutation({ mutationFn: () => api.acceptTaskOutcome(task!.id), onSuccess: async () => { await queryClient.invalidateQueries({ queryKey: ['tasks', task?.project_id] }); await queryClient.invalidateQueries({ queryKey: ['task', task?.id] }) } })
  if (!task) return <div className="inspector-empty"><ActivityIcon /><strong>当前 Thread 尚无 Run</strong><span>发送任务后，状态、恢复点和控制入口会显示在这里。</span></div>
  const latest = events.at(-1)
  return <section className="inspector-section" aria-label={task.goal}>
    <span className={`task-status-badge ${task.status}`}>{statusLabels[task.status] ?? task.status}</span><h3>{task.title || task.goal}</h3><p>{task.goal}</p>
    <dl className="inspector-facts"><div><dt>Run</dt><dd>{task.run_id.slice(0, 12)}</dd></div><div><dt>Mode</dt><dd>{task.mode}</dd></div><div><dt>事件</dt><dd>{events.length}</dd></div><div><dt>Cursor</dt><dd>{latest?.stream_seq ?? 0}</dd></div>{usage ? <><div><dt>模型用量</dt><dd>{usage.model_calls} 次 · {(usage.input_tokens + usage.output_tokens).toLocaleString()} Token</dd></div><div><dt>Latency</dt><dd>{usage.latency_ms.toLocaleString()} ms</dd></div></> : null}</dl>
    {task.error_code ? <div className="inspector-alert"><XCircle size={15} /><span><strong>{task.error_code}</strong><small>{task.result_summary || '打开 Trace 查看恢复信息。'}</small></span></div> : null}
    {task.status === 'succeeded' && task.result_summary ? <section className="inspector-result" aria-label="任务结果"><header><strong>任务结果</strong>{task.accepted ? <span><Check size={12} />已接受</span> : null}</header><p>{task.result_summary}</p>{!task.accepted ? <button className="primary-button" type="button" disabled={accept.isPending} onClick={() => accept.mutate()}><Check size={13} />接受成果</button> : null}{accept.error ? <small className="approval-error">{accept.error.message}</small> : null}</section> : null}
    {task.mode === 'agent_team' ? <InspectorAgentTeam room={agentTeam} /> : null}
    {artifacts.length ? <div className="inspector-artifacts">{artifacts.map((artifact) => <InspectorArtifactRow key={artifact.id} artifact={artifact} />)}</div> : null}
  </section>
}

function InspectorAgentTeam({ room }: { room?: AgentTeamRoom }) {
  const roleLabels = { coordinator: '协调者', researcher: '研究者', builder: '构建者', reviewer: '审查者' }
  return <section className="inspector-agent-team" aria-label="Agent Team 协作室"><header><strong>Agent Team 协作室</strong><small>group cursor {room?.next_cursor ?? 0}</small></header>{room?.members.length ? <div>{room.members.map((member) => <span key={member.id}><strong>{member.display_name}</strong><small>{roleLabels[member.role]} · {member.status}</small></span>)}</div> : <p>正在恢复成员状态…</p>}</section>
}

function InspectorArtifactRow({ artifact }: { artifact: Artifact }) {
  const [visibility, setVisibility] = useState<Visibility>('private')
  const queryClient = useQueryClient()
  const publish = useMutation({ mutationFn: () => api.publishArtifact(artifact.id, { name: artifact.name, visibility }), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['assets', artifact.project_id] }) })
  return <div className="artifact-publish-row"><span><strong>{artifact.name}</strong><small>{artifact.kind} · {artifact.media_type} · {artifact.size_bytes} B</small></span><a className="artifact-open-link" href={api.artifactPreviewUrl(artifact.id)} target="_blank" rel="noreferrer"><ExternalLink size={13} />直接打开</a><select aria-label={`${artifact.name} 发布可见性`} value={visibility} disabled={publish.isPending || publish.isSuccess} onChange={(event) => setVisibility(event.target.value as Visibility)}><option value="private">仅自己</option><option value="project">项目成员</option></select><button type="button" disabled={publish.isPending || publish.isSuccess} onClick={() => publish.mutate()}><FileUp size={13} />{publish.isSuccess ? '已发布' : publish.isPending ? '发布中' : '发布到资产'}</button></div>
}

export function InspectorContext({ projectModel, events }: { projectModel: string; events: RunEvent[] }) {
  const latestModel = [...events].reverse().map((event) => event.payload.model).find((value): value is string => typeof value === 'string' && value.trim() !== '')
  const provider = [...events].reverse().map((event) => event.payload.provider_id).find((value): value is string => typeof value === 'string' && value.trim() !== '')
  const thinking = [...events].reverse().map((event) => event.payload.thinking).find((value): value is string => typeof value === 'string' && value.trim() !== '')
  return <div className="inspector-section"><h3>冻结上下文</h3><p>显示当前 Run 已记录的有效配置；下一 Turn 的 Composer 变更不会改写本次快照。</p><dl className="inspector-facts"><div><dt>Model</dt><dd>{latestModel || projectModel || '自动选择'}</dd></div><div><dt>Provider</dt><dd>{provider || '默认实例'}</dd></div><div><dt>Thinking</dt><dd>{thinking || 'auto'}</dd></div><div><dt>消息策略</dt><dd>Session Leaf</dd></div></dl></div>
}

export function InspectorUsage({ usage }: { usage?: RunUsage }) {
  if (!usage) return <div className="inspector-empty"><Clock3 size={18} /><strong>尚无用量记录</strong><span>首次模型调用完成后更新。</span></div>
  return <div className="inspector-section"><h3>Run Usage</h3><dl className="inspector-facts"><div><dt>调用</dt><dd>{usage.model_calls}</dd></div><div><dt>输入</dt><dd>{usage.input_tokens.toLocaleString()}</dd></div><div><dt>输出</dt><dd>{usage.output_tokens.toLocaleString()}</dd></div><div><dt>Reasoning</dt><dd>{usage.reasoning_tokens.toLocaleString()}</dd></div><div><dt>Cache</dt><dd>{usage.cached_tokens.toLocaleString()}</dd></div><div><dt>Latency</dt><dd>{usage.latency_ms.toLocaleString()} ms</dd></div></dl><small className="inspector-note">Reasoning 为输出子集，不重复计入总 Token。</small></div>
}

export function InspectorEvents({ events, empty }: { events: RunEvent[]; empty: string }) {
  if (!events.length) return <div className="inspector-empty"><Workflow size={18} /><strong>{empty}</strong></div>
  return <div className="inspector-event-list">{events.map((event) => <article key={event.id}><span className="activity-node" /><div><strong>{event.event_type}</strong><small>#{event.stream_seq} · {new Date(event.occurred_at).toLocaleTimeString('zh-CN')}</small><code>{eventSummary(event.payload)}</code></div></article>)}</div>
}

export function InspectorSecurity({ projectId, taskId, approvals }: { projectId: string; taskId?: string; approvals: Approval[] }) {
  const relevant = taskId ? approvals.filter((approval) => approval.task_id === taskId) : []
  const [openedAt] = useState(() => Date.now())
  const queryClient = useQueryClient()
  const rules = useQuery({ queryKey: ['project-approval-rules', projectId], queryFn: () => api.projectApprovalRules(projectId) })
  const revoke = useMutation({ mutationFn: (rule: ApprovalRule) => api.revokeProjectApprovalRule(projectId, rule.id), onSuccess: () => queryClient.invalidateQueries({ queryKey: ['project-approval-rules', projectId] }) })
  const activeRules = (rules.data?.items ?? []).filter((rule) => !rule.revoked_at && new Date(rule.expires_at).getTime() > openedAt)
  return <div className="inspector-section"><h3>Security</h3><p>Full Access 和项目规则只减少逐次确认，确定性路径、网络和命令黑名单始终生效。</p>{relevant.length ? <div className="inspector-event-list">{relevant.map((approval) => <article key={approval.id}><span className={`activity-node ${approval.status}`} /><div><strong>{approval.tool_name}</strong><small>{approval.status} · {approval.risk_level}</small></div></article>)}</div> : <div className="inspector-empty compact"><LockKeyhole size={16} /><strong>当前 Run 无审批记录</strong></div>}<h3>项目审批规则</h3>{activeRules.length ? <div className="inspector-event-list">{activeRules.map((rule) => <article key={rule.id}><span className="activity-node approved" /><div><strong>{rule.tool_name}</strong><small>{rule.rule_key} · 至 {new Date(rule.expires_at).toLocaleString('zh-CN')}</small></div><button type="button" disabled={revoke.isPending} onClick={() => revoke.mutate(rule)}>撤销</button></article>)}</div> : <div className="inspector-empty compact"><LockKeyhole size={16} /><strong>没有生效中的项目规则</strong></div>}{revoke.error ? <small className="approval-error">{revoke.error.message}</small> : null}</div>
}

export function InspectorChanges({ runId, artifacts }: { runId?: string; artifacts: Artifact[] }) {
  const [baseline, setBaseline] = useState<'run_start' | 'head' | 'index'>('run_start')
  const changes = useQuery({ queryKey: ['run-changes', runId, baseline], queryFn: () => api.runChanges(runId!, baseline), enabled: Boolean(runId) })
  const fileArtifacts = artifacts.filter((artifact) => artifact.kind.includes('file') || artifact.name.includes('.') || artifact.media_type.startsWith('text/'))
  if (!runId) return <div className="inspector-empty"><FileIcon size={18} /><strong>当前 Run 尚无文件变化</strong><span>创建 Run 后会冻结工作区基线。</span></div>
  return <div className="inspector-section"><h3>Changes</h3><div className="changes-baseline-tabs" aria-label="Diff 基线">{([['run_start', 'Run 开始'], ['head', 'Git HEAD'], ['index', '当前工作区']] as const).map(([value, label]) => <button type="button" className={baseline === value ? 'active' : ''} key={value} onClick={() => setBaseline(value)}>{label}</button>)}</div>{changes.isLoading ? <p>正在读取受限 Diff…</p> : null}{changes.error ? <div className="panel-error">{changes.error.message}</div> : null}{changes.data?.baseline_notice ? <div className="panel-notice">{changes.data.baseline_notice}</div> : null}{changes.data ? <><div className="changes-meta"><span>{changes.data.baseline_head.slice(0, 12) || 'Index'}</span><span>→</span><span>{changes.data.current_head.slice(0, 12)}</span>{changes.data.truncated ? <em>已截断</em> : null}</div><pre className="changes-diff">{changes.data.diff || '该基线下没有文本变更。'}</pre></> : null}{fileArtifacts.length ? <div className="inspector-file-list">{fileArtifacts.map((artifact) => <a key={artifact.id} href={api.artifactContentUrl(artifact.id)} target="_blank" rel="noreferrer"><FileIcon size={14} /><span><strong>{artifact.name}</strong><small>{artifact.kind} · {artifact.media_type}</small></span><ChevronRight size={14} /></a>)}</div> : null}</div>
}

function eventSummary(payload: Record<string, unknown>) {
  const parts = ['tool_name', 'name', 'model', 'status', 'message'].flatMap((key) => {
    const value = payload[key]
    return typeof value === 'string' && value.trim() ? [`${key}: ${value}`] : []
  })
  return parts.slice(0, 3).join(' · ') || '已持久化事件'
}

function ActivityIcon() {
  return <span className="activity-node large" aria-hidden="true" />
}
