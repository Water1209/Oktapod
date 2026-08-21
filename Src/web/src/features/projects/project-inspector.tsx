import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, ChevronRight, Clock3, ExternalLink, File as FileIcon, FileUp, GitCommit, Layers, LockKeyhole, RotateCcw, Workflow, XCircle } from 'lucide-react'
import { api, type AgentTeamRoom, type Approval, type ApprovalRule, type Artifact, type RunEvent, type RunUsage, type Scene, type Task, type Visibility } from '../../shared/api/client'
import { parseUnifiedDiff, type RunDiffFile, type RunDiffHunk } from './run-diff'

const statusLabels: Record<string, string> = {
  queued: '待开始', running: '运行中', waiting_approval: '等待审批', waiting_input: '等待输入', paused: '已暂停', blocked: '执行受阻', succeeded: '已完成', failed: '失败', canceled: '已取消',
}

export function InspectorRun({ task, events, usage, artifacts, agentTeam }: { task?: Task; events: RunEvent[]; usage?: RunUsage; artifacts: Artifact[]; agentTeam?: AgentTeamRoom }) {
  const queryClient = useQueryClient()
  const accept = useMutation({ mutationFn: () => api.acceptTaskOutcome(task!.id), onSuccess: async () => { await queryClient.invalidateQueries({ queryKey: ['tasks', task?.project_id] }); await queryClient.invalidateQueries({ queryKey: ['task', task?.id] }) } })
  if (!task) return <div className="inspector-empty"><ActivityIcon /><strong>当前会话尚无任务运行</strong><span>发送任务后，状态、恢复点和控制入口会显示在这里。</span></div>
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
  const save = useMutation({ mutationFn: () => api.saveArtifactAsProjectFile(artifact.id, { name: artifact.name, visibility }), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['assets', artifact.project_id] }) })
  return <div className="artifact-publish-row"><span><strong>{artifact.name}</strong><small>{artifact.kind === 'evidence' ? '过程证据' : '交付物'} · {artifact.media_type} · {artifact.size_bytes} B</small></span><a className="artifact-open-link" href={api.artifactPreviewUrl(artifact.id)} target="_blank" rel="noreferrer"><ExternalLink size={13} />直接打开</a><select aria-label={`${artifact.name} 保存范围`} value={visibility} disabled={save.isPending || save.isSuccess} onChange={(event) => setVisibility(event.target.value as Visibility)}><option value="private">仅自己</option><option value="project">项目成员</option></select><button type="button" title="保存后供项目后续任务复用，不会发布到项目外" disabled={save.isPending || save.isSuccess} onClick={() => save.mutate()}><FileUp size={13} />{save.isSuccess ? '已保存到项目资料' : save.isPending ? '保存中' : '保存到项目资料'}</button></div>
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
  return <div className="inspector-section"><h3>安全</h3><p>完全访问和项目规则只减少逐次确认，确定性路径、网络和命令黑名单始终生效。</p>{relevant.length ? <div className="inspector-event-list">{relevant.map((approval) => <article key={approval.id}><span className={`activity-node ${approval.status}`} /><div><strong>{approval.tool_name}</strong><small>{approval.status} · {approval.risk_level}</small></div></article>)}</div> : <div className="inspector-empty compact"><LockKeyhole size={16} /><strong>当前 Run 无审批记录</strong></div>}<h3>项目审批规则</h3>{activeRules.length ? <div className="inspector-event-list">{activeRules.map((rule) => <article key={rule.id}><span className="activity-node approved" /><div><strong>{rule.tool_name}</strong><small>{rule.rule_key} · 至 {new Date(rule.expires_at).toLocaleString('zh-CN')}</small></div><button type="button" disabled={revoke.isPending} onClick={() => revoke.mutate(rule)}>撤销</button></article>)}</div> : <div className="inspector-empty compact"><LockKeyhole size={16} /><strong>没有生效中的项目规则</strong></div>}{revoke.error ? <small className="approval-error">{revoke.error.message}</small> : null}</div>
}

const baselineOptions = [
  { value: 'run_start', label: '任务开始', icon: Clock3, hint: 'Run 启动时冻结的提交到当前工作区，即本任务产生的全部改动（验收默认口径）', empty: '任务开始以来没有产生文本变更。' },
  { value: 'head', label: 'Git HEAD', icon: GitCommit, hint: '当前工作区相对最新提交，包含已暂存与未暂存的全部未提交改动', empty: '工作区与最新提交一致，没有未提交改动。' },
  { value: 'index', label: 'Git 暂存区', icon: Layers, hint: '当前工作区相对暂存区，只看尚未 git add 的改动', empty: '没有未暂存的工作区改动（工作区与暂存区一致）。' },
] as const

export function InspectorChanges({ task, artifacts, scene }: { task?: Task; artifacts: Artifact[]; scene: Scene }) {
  const runId = task?.run_id
  const [baseline, setBaseline] = useState<'run_start' | 'head' | 'index'>('run_start')
  const [selectedPaths, setSelectedPaths] = useState<string[]>([])
  const [commitMessage, setCommitMessage] = useState('')
  const [expandedFiles, setExpandedFiles] = useState<Record<string, boolean>>({})
  const queryClient = useQueryClient()
  const changes = useQuery({ queryKey: ['run-changes', runId, baseline], queryFn: () => api.runChanges(runId!, baseline), enabled: Boolean(runId) })
  const files = parseUnifiedDiff(changes.data?.diff ?? '')
  const selected = files.map((file) => file.path).filter((path) => selectedPaths.includes(path))
  const activeOption = baselineOptions.find((option) => option.value === baseline) ?? baselineOptions[0]
  const allSelected = files.length > 0 && files.every((file) => selectedPaths.includes(file.path))
  const allOpen = files.length > 0 && files.every((file, fileIndex) => expandedFiles[file.path] ?? fileIndex < 5)
  const accept = useMutation({
    mutationFn: () => api.acceptTaskOutcome(task!.id, '已在任务面板的变更页接受当前成果'),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['tasks', task?.project_id] })
      await queryClient.invalidateQueries({ queryKey: ['task', task?.id] })
    },
  })
  const governedAction = useMutation({
    mutationFn: (action: 'restore' | 'commit') => {
      if (!task?.conversation_id || !changes.data || selected.length === 0) throw new Error('请选择至少一个变更文件')
      const paths = selected.map((path) => `- ${path}`).join('\n')
      const stateGuard = `当前工作区状态哈希为 ${changes.data.status_hash}。先用 git_status 核对状态哈希；若已变化，停止并报告冲突，不得猜测或覆盖。`
      const content = action === 'restore'
        ? `撤销以下文件的当前工作区修改。${stateGuard}\n仅对这些路径逐项调用 git_restore，不修改其他文件；完成后用 git_status 和 git_diff 验证并报告。\n${paths}`
        : `保存以下已选文件的 Git 版本，提交信息为：${commitMessage.trim()}。${stateGuard}\n仅调用 git_commit_paths 提交这些路径，不包含其他工作区修改；完成后用 git_status 验证并报告提交哈希。\n${paths}`
      return api.appendConversationMessage(task.conversation_id, {
        content,
        acceptance: '操作必须受 Policy、审批、状态哈希与 Receipt 治理，只影响已选路径，并在原会话返回可核验结果。',
        mode: 'single_agent',
        visibility: 'private',
        permission_mode: 'ask',
        scene: 'code',
      })
    },
    onSuccess: async () => {
      setSelectedPaths([])
      setCommitMessage('')
      await queryClient.invalidateQueries({ queryKey: ['tasks', task?.project_id] })
      await queryClient.invalidateQueries({ queryKey: ['conversation-messages', task?.conversation_id] })
    },
  })
  const fileArtifacts = artifacts.filter((artifact) => artifact.kind.includes('file') || artifact.name.includes('.') || artifact.media_type.startsWith('text/'))
  if (!runId) return <div className="inspector-empty"><FileIcon size={18} /><strong>当前 Run 尚无文件变化</strong><span>创建 Run 后会冻结工作区基线。</span></div>
  const commitLabel = scene === 'work' ? '保存版本（Git 提交）' : '提交所选'
  return <div className="inspector-section">
    <h3>变更</h3>
    <div className="changes-baseline-tabs" aria-label="差异基线">
      {baselineOptions.map((option) => {
        const Icon = option.icon
        return <button type="button" className={baseline === option.value ? 'active' : ''} key={option.value} onClick={() => { setBaseline(option.value); setSelectedPaths([]) }}>
          <strong><Icon size={13} />{option.label}</strong>
          <small>{option.hint}</small>
        </button>
      })}
    </div>
    {changes.isLoading ? <p>正在读取受限差异…</p> : null}
    {changes.error ? <div className="panel-error">{changes.error.message}</div> : null}
    {changes.data?.baseline_notice ? <div className="panel-notice">{changes.data.baseline_notice}</div> : null}
    {changes.data ? <>
      <div className="changes-state-card">
        <span>{baselineMeta(baseline, changes.data.baseline_head, changes.data.current_head)}</span>
        {changes.data.truncated ? <em>差异过长 · 已截断</em> : null}
      </div>
      {files.length ? <>
        <div className="changes-toolbar">
          <span>{files.length} 个文件 · 已选 {selected.length}</span>
          <div>
            <button type="button" onClick={() => setSelectedPaths(allSelected ? [] : files.map((file) => file.path))}>{allSelected ? '取消全选' : '全选'}</button>
            <button type="button" onClick={() => setExpandedFiles(Object.fromEntries(files.map((file) => [file.path, !allOpen] as [string, boolean])))}>{allOpen ? '全部折叠' : '全部展开'}</button>
          </div>
        </div>
        <div className="changes-file-list">
          {files.map((file, fileIndex) => {
            const stats = diffStats(file)
            return <details key={file.path} open={expandedFiles[file.path] ?? fileIndex < 5} onToggle={(event) => {
              const open = event.currentTarget.open
              setExpandedFiles((current) => (current[file.path] === open ? current : { ...current, [file.path]: open }))
            }}>
              <summary>
                <input type="checkbox" aria-label={`选择 ${file.path}`} checked={selectedPaths.includes(file.path)} onClick={(event) => event.stopPropagation()} onChange={(event) => setSelectedPaths((current) => event.target.checked ? [...current, file.path] : current.filter((path) => path !== file.path))} />
                <span className="changes-file-path">{file.path}</span>
                <small className="diff-stat"><i className="add">+{stats.added}</i><i className="remove">−{stats.removed}</i> · {file.hunks.length} 块</small>
              </summary>
              {file.header.length ? <pre className="changes-file-header">{file.header.join('\n')}</pre> : null}
              {file.hunks.map((hunk, index) => <section className="changes-hunk" key={`${file.path}-${hunk.header}-${index}`}>
                <a href={`#change-${fileIndex}-${index}`} id={`change-${fileIndex}-${index}`}>{hunk.header}</a>
                <DiffHunkBody hunk={hunk} />
              </section>)}
            </details>
          })}
        </div>
      </> : <div className="inspector-empty compact"><Check size={16} /><strong>{activeOption.empty}</strong></div>}
      {baseline !== 'run_start' ? <p className="changes-baseline-note">基线只影响上方差异展示；“撤销所选”和“保存版本”始终作用于工作区文件，与所选基线无关。</p> : null}
      <div className="changes-actions">
        <button type="button" className="changes-accept" disabled={!task || task.accepted || accept.isPending} onClick={() => accept.mutate()}><Check size={13} />{task?.accepted ? '成果已接受' : '接受成果'}</button>
        <span className="changes-actions-divider" aria-hidden="true" />
        <button type="button" disabled={!selected.length || governedAction.isPending} onClick={() => governedAction.mutate('restore')}><RotateCcw size={13} />撤销所选</button>
        <input aria-label="Git 提交信息" value={commitMessage} onChange={(event) => setCommitMessage(event.target.value)} placeholder="提交信息" />
        <button type="button" disabled={!selected.length || !commitMessage.trim() || governedAction.isPending} onClick={() => governedAction.mutate('commit')}><GitCommit size={13} />{commitLabel}</button>
      </div>
      <small className="inspector-note">撤销与保存版本会在原会话创建受治理任务，并再次校验状态哈希、Policy、审批与 Receipt；浏览器不会直接改写 Git。</small>
      {accept.error || governedAction.error ? <div className="panel-error">{accept.error?.message || governedAction.error?.message}</div> : null}
    </> : null}
    {fileArtifacts.length ? <div className="inspector-file-list">{fileArtifacts.map((artifact) => <a key={artifact.id} href={api.artifactContentUrl(artifact.id)} target="_blank" rel="noreferrer"><FileIcon size={14} /><span><strong>{artifact.name}</strong><small>{artifact.kind} · {artifact.media_type}</small></span><ChevronRight size={14} /></a>)}</div> : null}
  </div>
}

function diffLineKind(line: string) {
  if (line.startsWith('+')) return 'add'
  if (line.startsWith('-')) return 'remove'
  if (line.startsWith('\\')) return 'meta'
  return 'ctx'
}

function diffStats(file: RunDiffFile) {
  let added = 0
  let removed = 0
  for (const hunk of file.hunks) {
    for (const line of hunk.lines) {
      if (line.startsWith('+')) added += 1
      else if (line.startsWith('-')) removed += 1
    }
  }
  return { added, removed }
}

function baselineMeta(baseline: 'run_start' | 'head' | 'index', baselineHead: string, currentHead: string) {
  if (baseline === 'index') return '对比：工作区 ↔ Git 暂存区（尚未 git add 的改动）'
  if (baseline === 'head') return `对比：工作区 ↔ 最新提交 HEAD ${currentHead.slice(0, 8) || '—'}`
  return `对比：任务开始 ${baselineHead.slice(0, 8) || '—'} ↔ 当前工作区（HEAD ${currentHead.slice(0, 8) || '—'}）`
}

function DiffHunkBody({ hunk }: { hunk: RunDiffHunk }) {
  const match = hunk.header.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/)
  let oldLine = match ? Number(match[1]) : 0
  let newLine = match ? Number(match[2]) : 0
  return <pre className="changes-diff-lines">{hunk.lines.map((line, index) => {
    const kind = diffLineKind(line)
    if (kind === 'meta') return <code key={index} className="diff-line meta"><span className="txt">{line}</span></code>
    const oldNo = kind === 'ctx' || kind === 'remove' ? String(oldLine++) : ''
    const newNo = kind === 'ctx' || kind === 'add' ? String(newLine++) : ''
    return <code key={index} className={`diff-line ${kind}`}><span className="ln">{oldNo}</span><span className="ln">{newNo}</span><span className="txt">{line || '\u00A0'}</span></code>
  })}</pre>
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
