import { useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, Bot, Braces, Cable, Check, CheckCircle2, ChevronDown, CircleAlert, Code2, KeyRound, MoreHorizontal, PackageOpen, Plus, Search, ShieldCheck, Trash2, Upload, Wrench, X } from 'lucide-react'
import { api, type Capability, type CapabilityScope } from '../../shared/api/client'
import { mcpPresetFromInput, TAVILY_MCP_PRESET, type ResourceInstallPreset } from './mcp-presets'

type ResourceView = 'installed' | 'system'
type ResourceFilter = 'all' | 'skill' | 'mcp' | 'agent'
type InstallType = 'skill' | 'mcp'

const systemResources = [
  { id: 'builtin-skill-creator', kind: 'skill', name: 'Skill Creator', version: 'builtin', status: 'enabled', availability: 'available', risk_level: 'L0', source_filename: 'Oktapod System', scope_type: 'global', manifest_json: '', permission_diff: [], content_hash: 'system:skill-creator' },
  ...['read_file', 'list_files', 'find_files', 'grep', 'edit_file', 'write_file', 'run_command', 'git_status', 'git_diff', 'git_log'].map((name) => ({ id: `builtin-tool-${name}`, kind: 'plugin', name, version: 'builtin', status: 'enabled', availability: 'available', risk_level: name === 'run_command' ? 'L2' : 'L0/L1', source_filename: 'Oktapod Core', scope_type: 'global', manifest_json: '', permission_diff: name === 'run_command' ? ['command'] : ['workspace'], content_hash: 'system:tool' })),
] as unknown as Capability[]

const availabilityText = { available: '可用', degraded: '需要修复', disabled: '已停用', quarantined: '待审核' } as const

export function ResourcesPage() {
  const queryClient = useQueryClient()
  const projects = useQuery({ queryKey: ['projects'], queryFn: api.projects })
  const [projectId, setProjectId] = useState('')
  const resolvedProjectId = projectId || projects.data?.items[0]?.id || ''
  const capabilities = useQuery({ queryKey: ['capabilities', resolvedProjectId], queryFn: () => api.capabilities(resolvedProjectId), enabled: Boolean(resolvedProjectId) })
  const agents = useQuery({ queryKey: ['agents', resolvedProjectId], queryFn: () => api.agents(resolvedProjectId), enabled: Boolean(resolvedProjectId) })
  const installed = useMemo(() => [
    ...(capabilities.data?.items ?? []),
    ...(agents.data?.items ?? []).map((agent) => ({ ...agent, kind: 'agent', availability: agent.status === 'enabled' ? 'available' : agent.status, risk_level: 'L0', source_filename: '项目 Agent 定义', scope_type: 'project', manifest_json: agent.config_json, permission_diff: [], content_hash: `agent:${agent.id}` } as unknown as Capability)),
  ], [agents.data, capabilities.data])
  const [view, setView] = useState<ResourceView>('installed')
  const [kind, setKind] = useState<ResourceFilter>('all')
  const [query, setQuery] = useState('')
  const [selectedId, setSelectedId] = useState<string>()
  const [installType, setInstallType] = useState<InstallType>()
  const [addOpen, setAddOpen] = useState(false)
  const [notice, setNotice] = useState('')
  const [confirmDelete, setConfirmDelete] = useState(false)
  const source = view === 'installed' ? installed : systemResources
  const filtered = source.filter((item) => (kind === 'all' || item.kind === kind) && (!query.trim() || `${item.name} ${item.source_filename}`.toLowerCase().includes(query.trim().toLowerCase())))
  const selected = filtered.find((item) => item.id === selectedId) ?? filtered[0]
  const health = useQuery({ queryKey: ['mcp-health', selected?.id], queryFn: () => api.mcpHealth(selected!.id), enabled: Boolean(selected?.kind === 'mcp' && selected.status === 'enabled'), retry: false })

  const refresh = async () => Promise.all([
    queryClient.invalidateQueries({ queryKey: ['capabilities', resolvedProjectId] }),
    queryClient.invalidateQueries({ queryKey: ['agents', resolvedProjectId] }),
  ])
  const review = useMutation({ mutationFn: async (item: Capability) => {
    const approved = await api.reviewCapability(item.id, 'approved', '用户在资源中心核对来源、作用域、权限与凭据状态后批准')
    if (item.kind === 'mcp') await api.connectMCP(item.id)
    return approved
  }, onSettled: async () => { await refresh(); await queryClient.invalidateQueries({ queryKey: ['mcp-health', selected?.id] }) } })
  const repair = useMutation({ mutationFn: (item: Capability) => api.connectMCP(item.id), onSettled: async () => { await refresh(); await queryClient.invalidateQueries({ queryKey: ['mcp-health', selected?.id] }) } })
  const toggle = useMutation({ mutationFn: async (item: Capability) => {
    const next = item.status === 'enabled' ? 'disabled' : 'enabled'
    if (item.kind === 'agent') return api.setAgentStatus(item.id, next)
    if (item.kind === 'mcp' && next === 'disabled') await api.disconnectMCP(item.id).catch(() => undefined)
    const updated = await api.setCapabilityStatus(item.id, next)
    if (item.kind === 'mcp' && next === 'enabled') await api.connectMCP(item.id)
    return updated
  }, onSettled: async () => { await refresh(); await queryClient.invalidateQueries({ queryKey: ['mcp-health', selected?.id] }) } })
  const remove = useMutation({ mutationFn: async (item: Capability) => item.kind === 'agent' ? api.deleteAgent(item.id) : api.deleteCapability(item.id), onSuccess: async () => { setSelectedId(undefined); setConfirmDelete(false); setNotice('资源已永久删除；历史 Run 快照和脱敏审计仍保留。'); await refresh() } })

  const createSkillByConversation = () => {
    if (!resolvedProjectId) return
    window.sessionStorage.setItem(`oktapod.new-task-prefill.${resolvedProjectId}`, '请使用内置 Skill Creator 创建一个新 Skill。先询问我目标、触发条件、输入输出和需要的脚本或参考资料；生成单 Skill ZIP 后走资源中心校验与审批链安装。')
    window.location.assign(`/projects/${resolvedProjectId}?conversation=new`)
  }

  return <main className="management-page resource-center-page">
    <header className="management-heading resource-center-heading">
      <div><span className="eyebrow">Resources</span><h1>资源中心</h1><p>先判断能力是否可用，再完成添加、修复、停用或删除。</p></div>
      <div className="resource-heading-actions">
        <label className="inline-select"><span>项目</span><select value={resolvedProjectId} onChange={(event) => { setProjectId(event.target.value); setSelectedId(undefined) }}>{projects.data?.items.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</select></label>
        <div className="resource-add-menu"><button className="primary-button" type="button" disabled={!resolvedProjectId} onClick={() => setAddOpen((value) => !value)}><Plus size={15} />添加资源<ChevronDown size={13} /></button>{addOpen ? <div role="menu"><button type="button" onClick={createSkillByConversation}><Braces size={15} /><span><strong>通过对话创建 Skill</strong><small>使用内置 Skill Creator</small></span></button><button type="button" onClick={() => { setInstallType('skill'); setAddOpen(false) }}><Upload size={15} /><span><strong>导入 Skill ZIP</strong><small>单 Skill 安全校验</small></span></button><button type="button" onClick={() => { setInstallType('mcp'); setAddOpen(false) }}><Cable size={15} /><span><strong>添加 MCP</strong><small>名称、URL、包或命令</small></span></button></div> : null}</div>
      </div>
    </header>
    {notice ? <div className="resource-install-notice" role="status"><CheckCircle2 size={16} />{notice}<button type="button" aria-label="关闭提示" onClick={() => setNotice('')}><X size={14} /></button></div> : null}
    <div className="resource-toolbar">
      <div className="resource-view-tabs"><button className={view === 'installed' ? 'active' : ''} onClick={() => { setView('installed'); setSelectedId(undefined) }}>已安装 <em>{installed.length}</em></button><button className={view === 'system' ? 'active' : ''} onClick={() => { setView('system'); setSelectedId(undefined) }}>系统内置</button></div>
      <div className="resource-kind-tabs">{(['all', 'skill', 'mcp', 'agent'] as ResourceFilter[]).map((value) => <button className={kind === value ? 'active' : ''} key={value} onClick={() => { setKind(value); setSelectedId(undefined) }}>{value === 'all' ? '全部' : value === 'skill' ? 'Skill' : value.toUpperCase()}</button>)}</div>
      <label className="resource-search"><Search size={14} /><input aria-label="搜索资源" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索" /></label>
    </div>
    <div className={`resource-layout-v2 ${selectedId ? 'has-selection' : ''}`}>
      <section className="resource-list-v2" aria-label="资源列表">
        {filtered.map((item) => <button type="button" className={item.id === selected?.id ? 'active' : ''} key={item.id} onClick={() => setSelectedId(item.id)}><ResourceIcon item={item} /><span><strong>{item.name}</strong><small>{item.kind === 'plugin' ? '内置 Tool' : item.kind} · {item.source_filename || '项目定义'}</small></span><em className={item.availability ?? item.status}><i />{availabilityText[(item.availability ?? (item.status === 'enabled' ? 'available' : item.status)) as keyof typeof availabilityText] ?? item.status}</em></button>)}
        {!filtered.length ? <div className="master-empty"><PackageOpen size={22} /><strong>没有匹配资源</strong><span>{view === 'installed' ? '使用“添加资源”安装能力。' : '调整筛选查看系统资源。'}</span></div> : null}
      </section>
      <section className="resource-detail-v2">{selected ? <>
        <button className="resource-mobile-back" type="button" onClick={() => setSelectedId(undefined)}><ArrowLeft size={16} />返回列表</button>
        <header><ResourceIcon item={selected} /><div><span>{selected.kind === 'plugin' ? '系统 Tool' : selected.kind.toUpperCase()}</span><h2>{selected.name}</h2><p>{selected.source_filename || '项目定义'} · v{selected.version}</p></div>{view === 'installed' ? <div className="resource-detail-actions"><label className="resource-toggle"><input type="checkbox" checked={selected.status === 'enabled'} disabled={selected.status === 'quarantined' || toggle.isPending} onChange={() => toggle.mutate(selected)} /><span /></label><button aria-label="更多操作" onClick={() => setConfirmDelete((value) => !value)}><MoreHorizontal size={17} /></button></div> : null}</header>
        <Lifecycle availability={selected.availability ?? (selected.status === 'enabled' ? 'available' : selected.status)} />
        {confirmDelete ? <div className="resource-delete-confirm" role="alert"><Trash2 size={17} /><span><strong>永久删除此资源？</strong><small>活动任务正在引用时会被阻止；历史快照和脱敏审计不会删除。</small></span><button type="button" onClick={() => setConfirmDelete(false)}>取消</button><button className="danger" type="button" disabled={remove.isPending} onClick={() => remove.mutate(selected)}>确认删除</button></div> : null}
        {selected.availability === 'degraded' || health.error ? <div className="resource-repair"><CircleAlert size={17} /><span><strong>需要修复</strong><small>{health.error?.message || health.data?.last_error || (selected.credential_configured ? '连接或健康检查未通过。' : '缺少安全凭据。')}</small></span>{selected.kind === 'mcp' ? <button className="primary-button" disabled={repair.isPending} onClick={() => repair.mutate(selected)}><Wrench size={14} />重新验证</button> : null}</div> : null}
        <div className="resource-detail-grid"><section><h3>工具与权限</h3><div className="trust-note"><ShieldCheck size={17} /><span><strong>{selected.risk_level || 'L0'} 风险级别</strong><small>{selected.permission_diff?.length ? selected.permission_diff.join('、') : '未声明额外扩权；运行时策略仍逐次检查。'}</small></span></div>{health.data ? <p className="resource-tool-summary">发现 <strong>{health.data.tool_count}</strong> 个工具 · 协议 {health.data.transport}</p> : null}</section><section><h3>配置与凭据</h3><dl><div><dt>范围</dt><dd>{selected.scope_type === 'global' ? '全局' : '当前项目'}</dd></div><div><dt>凭据</dt><dd>{selected.kind === 'mcp' ? selected.credential_configured ? 'SecretStore 已配置' : '未配置' : '不需要'}</dd></div><div><dt>内容</dt><dd><code>{selected.content_hash}</code></dd></div></dl></section></div>
        {selected.status === 'quarantined' && view === 'installed' ? <button className="primary-button resource-primary-action" disabled={review.isPending} onClick={() => review.mutate(selected)}><Check size={15} />审核并启用</button> : null}
        {review.error || repair.error || toggle.error || remove.error ? <p className="form-error">{review.error?.message ?? repair.error?.message ?? toggle.error?.message ?? remove.error?.message}</p> : null}
      </> : <div className="detail-empty"><PackageOpen size={28} /><h2>选择资源查看详情</h2></div>}</section>
    </div>
    {installType ? <ResourceInstallDialog projectId={resolvedProjectId} preset={installType === 'mcp' ? undefined : undefined} initialType={installType} onClose={() => setInstallType(undefined)} onInstalled={async (item, message) => { await refresh(); setView('installed'); setKind(item.kind === 'skill' ? 'skill' : 'mcp'); setSelectedId(item.id); setNotice(message); setInstallType(undefined) }} /> : null}
  </main>
}

function ResourceIcon({ item }: { item: Capability }) {
  const Icon = item.kind === 'mcp' ? Cable : item.kind === 'agent' ? Bot : item.kind === 'plugin' ? Code2 : Braces
  return <span className={`resource-kind ${item.kind}`}><Icon size={16} /></span>
}

function Lifecycle({ availability }: { availability: string }) {
  const validated = availability !== 'quarantined'
  const available = availability === 'available'
  const finalLabel = availability === 'degraded' ? '需要修复' : availability === 'disabled' ? '已停用' : availability === 'quarantined' ? '待审核' : '可用'
  return <div className="resource-lifecycle" aria-label="资源生命周期"><span className="done"><i><Check size={12} /></i><strong>已安装</strong></span><b /><span className={validated ? 'done' : ''}><i>{validated ? <Check size={12} /> : '2'}</i><strong>{availability === 'quarantined' ? '待验证' : '已验证'}</strong></span><b /><span className={available ? 'done' : availability === 'degraded' ? 'warning' : ''}><i>{available ? <Check size={12} /> : '3'}</i><strong>{finalLabel}</strong></span></div>
}

export function ResourceInstallDialog({ projectId, preset, initialType, onClose, onInstalled }: { projectId: string; preset?: ResourceInstallPreset; initialType?: InstallType; onClose: () => void; onInstalled: (item: Capability, notice: string) => void | Promise<void> }) {
  const type = preset?.type === 'mcp' ? 'mcp' : initialType ?? 'skill'
  const credentialPrefilled = Boolean(preset?.credential)
  const fileRef = useRef<HTMLInputElement>(null)
  const credentialInputRef = useRef<HTMLInputElement>(null)
  const credentialBuffer = useRef(preset?.credential ?? '')
  const [scope, setScope] = useState<CapabilityScope>(preset?.scope ?? 'project')
  const [file, setFile] = useState<File>()
  const [source, setSource] = useState(preset?.input ?? preset?.name ?? '')
  const [resolved, setResolved] = useState<ResourceInstallPreset | undefined>(preset)
  const [manifest, setManifest] = useState(preset?.manifest ?? '')
  const [validated, setValidated] = useState(false)
  const [validationError, setValidationError] = useState('')
  const resolveMCP = () => {
    try { const result = mcpPresetFromInput(source); setResolved(result); setManifest(result.manifest); setScope(result.scope ?? 'project'); setValidated(false); setValidationError('') } catch (error) { setValidationError(error instanceof Error ? error.message : '无法解析 MCP 来源。') }
  }
  const validate = () => {
    setValidationError('')
    if (type === 'skill') {
      if (!file || !file.name.toLowerCase().endsWith('.zip')) { setValidationError('Skill 只支持导入单 Skill ZIP。'); return }
      if (file.size > 50 * 1024 * 1024) { setValidationError('ZIP 超过 50 MiB 安全上限。'); return }
    } else {
      try { JSON.parse(manifest); if (!resolved) throw new Error('请先解析 MCP 来源。') } catch (error) { setValidationError(error instanceof Error ? error.message : 'MCP 配置必须是有效 JSON。'); return }
    }
    if (credentialInputRef.current) credentialInputRef.current.value = ''
    setValidated(true)
  }
  const install = useMutation({ mutationFn: async () => {
    if (type === 'skill') return api.createCapability(projectId, { kind: 'skill', name: file!.name.replace(/\.zip$/i, ''), version: '0.1.0', manifest_json: '{}', scope_type: scope, source_filename: file!.name, bundle_base64: await fileToBase64(file!) })
    return api.createCapability(projectId, { kind: 'mcp', name: resolved!.name, version: resolved!.version, manifest_json: manifest, scope_type: scope, source_filename: resolved!.manifest.includes('"source"') ? resolved!.name : 'manual', credential: credentialBuffer.current.trim() || undefined })
  }, onSuccess: (item) => onInstalled(item, `“${item.name}”已进入统一安装链；当前状态：${availabilityText[item.availability] ?? item.status}。`) })

  return <div className="resource-install-overlay"><section className="resource-install-dialog resource-install-panel" role="dialog" aria-modal="true" aria-labelledby="resource-install-title">
    <header><div><span className="eyebrow">{type === 'skill' ? 'Skill ZIP' : 'MCP Definition v1'}</span><h2 id="resource-install-title">{type === 'skill' ? '导入 Skill' : '添加 MCP'}</h2><p>{type === 'skill' ? '只接受一个完整 Skill；不执行压缩包内容。' : '输入名称、URL、npm/PyPI 包或本地命令，系统会生成统一配置。'}</p></div><button type="button" aria-label="关闭安装窗口" onClick={onClose}><X size={18} /></button></header>
    <div className="resource-scope-choice"><button className={scope === 'project' ? 'active' : ''} onClick={() => { setScope('project'); setValidated(false) }}><strong>当前项目</strong><small>只对这个项目可用</small></button><button className={scope === 'global' ? 'active' : ''} onClick={() => { setScope('global'); setValidated(false) }}><strong>全局</strong><small>所有个人项目可用</small></button></div>
    {type === 'skill' ? <><button className="resource-file-drop" type="button" onClick={() => fileRef.current?.click()}><Upload size={20} /><span><strong>{file?.name || '选择单 Skill ZIP'}</strong><small>校验 SKILL.md、文件树、脚本风险和压缩安全</small></span></button><input ref={fileRef} hidden type="file" accept="application/zip,.zip" onChange={(event) => { setFile(event.target.files?.[0]); setValidated(false); setValidationError('') }} /><div className="resource-validation-rules"><span><Check size={13} />必须包含一份 SKILL.md</span><span><Check size={13} />允许 agents / scripts / references / assets</span><span><X size={13} />拒绝嵌套 Skill、node_modules、隐藏载荷和路径穿越</span></div></> : <>
      <label className="resource-source-input"><span>名称、URL、包或命令</span><div><input autoFocus value={source} onChange={(event) => { setSource(event.target.value); setResolved(undefined); setValidated(false) }} placeholder="例如 Tavily、https://…/mcp、npx -y @modelcontextprotocol/server-filesystem@1.0.0 D:\\safe" /><button type="button" onClick={resolveMCP}>解析</button></div></label>
      {!resolved && /tavily/i.test(source) ? <button className="mcp-preset-button" type="button" onClick={() => { setSource('Tavily'); setResolved(TAVILY_MCP_PRESET); setManifest(TAVILY_MCP_PRESET.manifest); setScope('global') }}><Cable size={15} /><span><strong>使用 Tavily 官方远端服务</strong><small>Streamable HTTP · Bearer · 无需本地进程</small></span></button> : null}
      {resolved ? <div className="mcp-resolved-card"><span><CheckCircle2 size={16} /></span><div><strong>{resolved.name}</strong><small>{manifest.includes('streamable_http') ? '远端 Streamable HTTP' : '本地 stdio'} · {resolved.scope === 'global' ? '全局' : '项目'}</small></div><em>已解析</em></div> : null}
      {resolved ? <label className="resource-credential-field"><span><KeyRound size={14} />安全凭据 <em>仅写入 SecretStore；校验后立即清空输入框</em></span><input ref={credentialInputRef} type="password" autoComplete="new-password" onChange={(event) => { credentialBuffer.current = event.target.value; setValidated(false) }} placeholder={credentialPrefilled ? '已从对话安全暂存，可直接校验' : resolved.name === 'tavily' ? 'Tavily API Key' : '无认证可留空'} /></label> : null}
      {resolved ? <details className="resource-advanced"><summary>高级配置</summary><textarea value={manifest} onChange={(event) => { setManifest(event.target.value); setValidated(false) }} rows={9} spellCheck={false} /></details> : null}
    </>}
    {validated ? <div className="resource-install-validation"><strong>格式预检通过</strong><span>{type === 'skill' ? '服务端将执行完整 ZIP 安全扫描；带脚本或高风险权限的 Skill 会进入待审核状态。' : '安装前会固定来源和版本；Secret 不会进入消息、manifest、日志或数据库明文字段。'}</span></div> : null}
    {validationError ? <p className="form-error">{validationError}</p> : null}{install.error ? <p className="form-error">{install.error.message}</p> : null}
    <footer><span>安装、验证、审批、启用和调用使用同一资源生命周期。</span>{validated ? <button className="primary-button" disabled={install.isPending} onClick={() => install.mutate()}>{install.isPending ? '正在安装…' : '确认安装'}</button> : <button className="primary-button" disabled={type === 'skill' ? !file : !resolved} onClick={validate}>校验配置</button>}</footer>
  </section></div>
}

function fileToBase64(file: File) {
  return new Promise<string>((resolve, reject) => { const reader = new FileReader(); reader.onerror = () => reject(new Error('无法读取所选 ZIP。')); reader.onload = () => resolve(String(reader.result).split(',', 2)[1] || ''); reader.readAsDataURL(file) })
}
