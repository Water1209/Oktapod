import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useSearch } from '@tanstack/react-router'
import { ArrowLeft, Bot, Braces, Cable, Check, CheckCircle2, ChevronDown, CircleAlert, Code2, KeyRound, MoreHorizontal, PackageOpen, Plus, Search, ShieldCheck, Trash2, Upload, Wrench, X } from 'lucide-react'
import { api, type Capability, type CapabilityScope, type MCPDefinitionV1, type MCPInstallCandidate, type MCPOAuthSession, type ResourceInstallDraft } from '../../shared/api/client'
import { mcpSecretBindings } from './mcp-secret-bindings'
import { filterProjectResources, filterSystemResources, resourceEmptyCopy, resourceKindCounts, type ResourceKind, type ResourceView } from './resource-filter'

type InstallType = 'skill' | 'mcp'

const systemResources = [
  { id: 'builtin-skill-creator', kind: 'skill', name: 'Skill Creator', version: 'builtin', status: 'enabled', availability: 'available', risk_level: 'L0', source_filename: 'Oktapod System', scope_type: 'global', manifest_json: '', permission_diff: [], content_hash: 'system:skill-creator' },
  ...['read_file', 'list_files', 'find_files', 'grep', 'edit_file', 'write_file', 'run_command', 'git_status', 'git_diff', 'git_log'].map((name) => ({ id: `builtin-tool-${name}`, kind: 'plugin', name, version: 'builtin', status: 'enabled', availability: 'available', risk_level: name === 'run_command' ? 'L2' : 'L0/L1', source_filename: 'Oktapod Core', scope_type: 'global', manifest_json: '', permission_diff: name === 'run_command' ? ['command'] : ['workspace'], content_hash: 'system:tool' })),
] as unknown as Capability[]

const availabilityText = { available: '可用', degraded: '需要修复', disabled: '已停用', quarantined: '待审核' } as const

export function ResourcesPage() {
  const queryClient = useQueryClient()
  const search = useSearch({ from: '/resources' })
  const projects = useQuery({ queryKey: ['projects'], queryFn: api.projects })
  const [projectId, setProjectId] = useState('')
  const resolvedProjectId = projectId || search.project || projects.data?.items[0]?.id || ''
  const capabilities = useQuery({ queryKey: ['capabilities', resolvedProjectId], queryFn: () => api.capabilities(resolvedProjectId), enabled: Boolean(resolvedProjectId) })
  const agents = useQuery({ queryKey: ['agents', resolvedProjectId], queryFn: () => api.agents(resolvedProjectId), enabled: Boolean(resolvedProjectId) })
  const installed = useMemo(() => [
    ...(capabilities.data?.items ?? []),
    ...(agents.data?.items ?? []).map((agent) => ({ ...agent, kind: 'agent', availability: agent.status === 'enabled' ? 'available' : agent.status, risk_level: 'L0', source_filename: '项目 Agent 定义', scope_type: 'project', manifest_json: agent.config_json, permission_diff: [], content_hash: `agent:${agent.id}` } as unknown as Capability)),
  ], [agents.data, capabilities.data])
  const [view, setView] = useState<ResourceView>(search.view ?? 'project')
  const [kind, setKind] = useState<ResourceKind>(search.kind ?? (search.resource ? 'mcp' : 'all'))
  const [query, setQuery] = useState('')
  const [selectedId, setSelectedId] = useState<string | undefined>(search.resource)
  const [installType, setInstallType] = useState<InstallType>()
  const [addOpen, setAddOpen] = useState(false)
  const [notice, setNotice] = useState('')
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [oauthSession, setOAuthSession] = useState<MCPOAuthSession>()
  const [credentialEditorId, setCredentialEditorId] = useState(search.resource ?? '')
  const repairCredentialInputRefs = useRef<Record<string, HTMLInputElement | null>>({})
  const repairCredentialBuffer = useRef<Record<string, string>>({})
  const oauthPopup = useRef<Window | null>(null)
  const counts = useMemo(() => resourceKindCounts(installed), [installed])
  const filtered = view === 'project' ? filterProjectResources(installed, kind, query) : filterSystemResources(systemResources, query)
  const selected = filtered.find((item) => item.id === selectedId) ?? filtered[0]
  const emptyCopy = resourceEmptyCopy(view, kind, Boolean(query.trim()))
  const selectedUsesOAuth = isOAuthMCP(selected)
  const selectedSecretBindings = mcpSecretBindings(selected)
  const health = useQuery({ queryKey: ['mcp-health', selected?.id], queryFn: () => api.mcpHealth(selected!.id), enabled: Boolean(selected?.kind === 'mcp' && selected.status === 'enabled'), retry: false })
  const oauthStatus = useQuery({
    queryKey: ['mcp-oauth', selected?.id, oauthSession?.id],
    queryFn: () => api.mcpOAuthStatus(selected!.id, oauthSession!.id),
    enabled: Boolean(selected?.id && oauthSession?.id && (oauthSession.status === 'preparing' || oauthSession.status === 'waiting')),
    refetchInterval: (query) => {
      const status = query.state.data?.status
      return status === 'connected' || status === 'canceled' || status === 'failed' ? false : 1000
    },
    retry: false,
  })

  const refresh = async () => Promise.all([
    queryClient.invalidateQueries({ queryKey: ['capabilities', resolvedProjectId] }),
    queryClient.invalidateQueries({ queryKey: ['agents', resolvedProjectId] }),
  ])
  const review = useMutation({ mutationFn: async (item: Capability) => {
    const approved = await api.reviewCapability(item.id, 'approved', '用户在资源中心核对来源、作用域、权限与凭据状态后批准')
    if (item.kind === 'mcp' && !isOAuthMCP(item)) await api.connectMCP(item.id)
    return approved
  }, onSettled: async () => { await refresh(); await queryClient.invalidateQueries({ queryKey: ['mcp-health', selected?.id] }) } })
  const repair = useMutation({ mutationFn: (item: Capability) => api.connectMCP(item.id), onSettled: async () => { await refresh(); await queryClient.invalidateQueries({ queryKey: ['mcp-health', selected?.id] }) } })
  const saveCredential = useMutation({ mutationFn: async (item: Capability) => {
    const credentials = Object.fromEntries(selectedSecretBindings.map((binding) => [binding.name, repairCredentialBuffer.current[binding.name] ?? '']))
    const updated = await api.updateMCPCredential(item.id, credentials)
    try {
      await api.connectMCP(item.id)
      return { updated, connected: true }
    } catch (error) {
      return { updated, connected: false, error: error instanceof Error ? error.message : '连接验证失败' }
    }
  }, onSuccess: async (result) => {
    repairCredentialBuffer.current = {}
    Object.values(repairCredentialInputRefs.current).forEach((input) => { if (input) input.value = '' })
    setCredentialEditorId('')
    setNotice(result.connected ? 'MCP 凭据已写入数据库密钥库，连接验证通过。' : `MCP 凭据已安全保存；连接仍不可用：${result.error}`)
    await refresh()
    await queryClient.invalidateQueries({ queryKey: ['mcp-health', selected?.id] })
  } })
  const toggle = useMutation({ mutationFn: async (item: Capability) => {
    const next = item.status === 'enabled' ? 'disabled' : 'enabled'
    if (item.kind === 'agent') return api.setAgentStatus(item.id, next)
    if (item.kind === 'mcp' && next === 'disabled') await api.disconnectMCP(item.id).catch(() => undefined)
    const updated = await api.setCapabilityStatus(item.id, next)
    if (item.kind === 'mcp' && next === 'enabled' && !isOAuthMCP(item)) await api.connectMCP(item.id)
    return updated
  }, onSettled: async () => { await refresh(); await queryClient.invalidateQueries({ queryKey: ['mcp-health', selected?.id] }) } })
  const remove = useMutation({ mutationFn: async (item: Capability) => item.kind === 'agent' ? api.deleteAgent(item.id) : api.deleteCapability(item.id), onSuccess: async () => { setSelectedId(undefined); setConfirmDelete(false); setNotice('资源已永久删除；历史 Run 快照和脱敏审计仍保留。'); await refresh() } })
  const oauthStart = useMutation({ mutationFn: (item: Capability) => api.startMCPOAuth(item.id), onSuccess: (session) => {
    setOAuthSession(session)
    if (session.authorization_url && oauthPopup.current) oauthPopup.current.location.assign(session.authorization_url)
    if (session.status !== 'preparing' && session.status !== 'waiting') oauthPopup.current?.close()
  }, onError: () => { oauthPopup.current?.close(); oauthPopup.current = null } })
  const oauthCancel = useMutation({ mutationFn: ({ item, session }: { item: Capability; session: MCPOAuthSession }) => api.cancelMCPOAuth(item.id, session.id), onSuccess: (session) => setOAuthSession(session) })
  const activeOAuthSession = oauthStatus.data ?? oauthSession

  useEffect(() => {
    const session = oauthStatus.data
    if (!session) return
    if (session.status === 'preparing' || session.status === 'waiting') return
    oauthPopup.current?.close()
    oauthPopup.current = null
    void refresh()
    void queryClient.invalidateQueries({ queryKey: ['mcp-health', session.capability_id] })
  // refresh is intentionally represented by its stable dependencies.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [oauthStatus.data, queryClient, resolvedProjectId])

  const beginOAuth = (item: Capability) => {
    oauthPopup.current?.close()
    oauthPopup.current = window.open('about:blank', 'oktapod-mcp-oauth', 'popup=yes,width=720,height=760')
    oauthStart.mutate(item)
  }

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
      <div className="resource-view-tabs"><button className={view === 'project' ? 'active' : ''} onClick={() => { setView('project'); setSelectedId(undefined) }}>项目资源 <em>{installed.length}</em></button><button className={view === 'system' ? 'active' : ''} onClick={() => { setView('system'); setKind('all'); setSelectedId(undefined) }}>系统能力 <em>{systemResources.length}</em></button></div>
      {view === 'project' ? <div className="resource-kind-tabs">{(['all', 'skill', 'mcp', 'agent'] as ResourceKind[]).map((value) => <button className={kind === value ? 'active' : ''} key={value} onClick={() => { setKind(value); setSelectedId(undefined) }}>{value === 'all' ? '全部' : value === 'skill' ? 'Skill' : value.toUpperCase()} <em>{counts[value]}</em></button>)}</div> : <div className="resource-scope-note">Oktapod 随版本提供</div>}
      <label className="resource-search"><Search size={14} /><input aria-label="搜索资源" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索" /></label>
    </div>
    <div className={`resource-layout-v2 ${selectedId ? 'has-selection' : ''}`}>
      <section className="resource-list-v2" aria-label="资源列表">
        {filtered.map((item) => <button type="button" className={item.id === selected?.id ? 'active' : ''} key={item.id} onClick={() => { if (item.id !== selected?.id) { oauthPopup.current?.close(); oauthPopup.current = null; setOAuthSession(undefined) } setSelectedId(item.id) }}><ResourceIcon item={item} /><span><strong>{item.name}</strong><small>{item.kind === 'plugin' ? '内置 Tool' : item.kind} · {item.source_filename || '项目定义'}</small></span><em className={item.availability ?? item.status}><i />{availabilityText[(item.availability ?? (item.status === 'enabled' ? 'available' : item.status)) as keyof typeof availabilityText] ?? item.status}</em></button>)}
        {!filtered.length ? <div className="master-empty"><PackageOpen size={22} /><strong>{emptyCopy.title}</strong><span>{emptyCopy.detail}</span>{view === 'project' && kind !== 'all' && !query.trim() ? <button type="button" onClick={() => setKind('all')}>显示全部项目资源</button> : null}</div> : null}
      </section>
      <section className="resource-detail-v2">{selected ? <>
        <button className="resource-mobile-back" type="button" onClick={() => setSelectedId(undefined)}><ArrowLeft size={16} />返回列表</button>
        <header><ResourceIcon item={selected} /><div><span>{selected.kind === 'plugin' ? '系统 Tool' : selected.kind.toUpperCase()}</span><h2>{selected.name}</h2><p>{selected.source_filename || '项目定义'} · v{selected.version}</p></div>{view === 'project' ? <div className="resource-detail-actions"><label className="resource-toggle"><input type="checkbox" checked={selected.status === 'enabled'} disabled={selected.status === 'quarantined' || toggle.isPending} onChange={() => toggle.mutate(selected)} /><span /></label><button aria-label="更多操作" onClick={() => setConfirmDelete((value) => !value)}><MoreHorizontal size={17} /></button></div> : null}</header>
        <Lifecycle availability={selected.availability ?? (selected.status === 'enabled' ? 'available' : selected.status)} />
        {confirmDelete ? <div className="resource-delete-confirm" role="alert"><Trash2 size={17} /><span><strong>永久删除此资源？</strong><small>活动任务正在引用时会被阻止；历史快照和脱敏审计不会删除。</small></span><button type="button" onClick={() => setConfirmDelete(false)}>取消</button><button className="danger" type="button" disabled={remove.isPending} onClick={() => remove.mutate(selected)}>确认删除</button></div> : null}
        {selected.availability === 'degraded' || health.error ? <div className="resource-repair"><CircleAlert size={17} /><span><strong>{selectedUsesOAuth ? '需要连接账号' : selected.credential_state === 'missing' ? '凭据缺失' : selected.credential_state === 'locked' ? '密钥库已锁定' : selected.credential_state === 'corrupt' ? '凭据密文损坏' : '需要修复'}</strong><small>{selectedUsesOAuth ? (activeOAuthSession?.last_error || '通过服务商授权页连接账号；令牌只保存在数据库密钥库。') : health.error?.message || health.data?.last_error || credentialStateText(selected)}</small></span>{selected.kind === 'mcp' ? selectedUsesOAuth ? <button className="primary-button" disabled={oauthStart.isPending || activeOAuthSession?.status === 'waiting'} onClick={() => beginOAuth(selected)}><KeyRound size={14} />连接账号</button> : selected.credential_state === 'missing' || selected.credential_state === 'corrupt' ? <button className="primary-button" onClick={() => setCredentialEditorId(selected.id)}><KeyRound size={14} />重新录入</button> : selected.credential_state === 'locked' ? <a className="primary-button" href="/settings/vault"><KeyRound size={14} />恢复 Vault</a> : <button className="primary-button" disabled={repair.isPending} onClick={() => repair.mutate(selected)}><Wrench size={14} />重新验证</button> : null}</div> : null}
        {selected.kind === 'mcp' && !selectedUsesOAuth && credentialEditorId === selected.id ? <form className="resource-credential-reentry" onSubmit={(event) => { event.preventDefault(); saveCredential.mutate(selected) }}><header><div><strong>重新录入 MCP 凭据</strong><small>值只发送到数据库密钥库，不会写入 manifest、页面存储或日志。</small></div><button type="button" aria-label="关闭凭据录入" onClick={() => setCredentialEditorId('')}><X size={14} /></button></header>{selectedSecretBindings.map((binding) => <label className="resource-credential-field" key={`${binding.name}:${binding.target}`}><span>{binding.name}{binding.required ? <b>必需</b> : <em>可选</em>}<small>{binding.target.startsWith('env:') ? '注入本地 MCP 进程' : '注入远端请求 Header'}</small></span><input ref={(input) => { repairCredentialInputRefs.current[binding.name] = input }} required={binding.required} type="password" autoComplete="new-password" placeholder="输入新凭据" onChange={(event) => { repairCredentialBuffer.current[binding.name] = event.target.value }} /></label>)}<footer><button className="secondary-button compact" type="button" onClick={() => setCredentialEditorId('')}>取消</button><button className="primary-button compact" type="submit" disabled={saveCredential.isPending || selectedSecretBindings.length === 0}>{saveCredential.isPending ? '保存并验证中…' : '保存并测试连接'}</button></footer></form> : null}
        {selectedUsesOAuth && activeOAuthSession?.capability_id === selected.id && (activeOAuthSession.status === 'preparing' || activeOAuthSession.status === 'waiting') ? <div className="resource-oauth-session" role="status"><span><strong>等待浏览器授权</strong><small>授权完成后此处会自动验证 MCP；关闭授权页不会删除资源。</small></span>{activeOAuthSession.authorization_url ? <a href={activeOAuthSession.authorization_url} target="_blank" rel="noreferrer">打开授权页</a> : null}<button type="button" disabled={oauthCancel.isPending} onClick={() => oauthCancel.mutate({ item: selected, session: activeOAuthSession })}>取消授权</button></div> : null}
        <div className="resource-detail-grid"><section><h3>工具与权限</h3><div className="trust-note"><ShieldCheck size={17} /><span><strong>{selected.risk_level || 'L0'} 风险级别</strong><small>{selected.permission_diff?.length ? selected.permission_diff.join('、') : '未声明额外扩权；运行时策略仍逐次检查。'}</small></span></div>{health.data ? <p className="resource-tool-summary">发现 <strong>{health.data.tool_count}</strong> 个工具 · 协议 {health.data.transport}</p> : null}</section><section><h3>配置与凭据</h3><dl><div><dt>范围</dt><dd>{selected.scope_type === 'global' ? '全局' : '当前项目'}</dd></div><div><dt>{selectedUsesOAuth ? '账号' : '凭据'}</dt><dd>{selected.kind === 'mcp' ? selectedUsesOAuth && selected.credential_state === 'ready' ? 'OAuth 令牌可用' : credentialStateLabel[selected.credential_state] : '不需要'}</dd></div><div><dt>内容</dt><dd><code>{selected.content_hash}</code></dd></div></dl>{selected.kind === 'mcp' ? <div className="resource-credential-actions">{selectedUsesOAuth ? <button className="secondary-button compact" type="button" disabled={oauthStart.isPending} onClick={() => beginOAuth(selected)}><KeyRound size={14} />{selected.credential_state === 'ready' ? '重新连接账号' : '连接账号'}</button> : selectedSecretBindings.length ? <button className="secondary-button compact" type="button" onClick={() => setCredentialEditorId(selected.id)}><KeyRound size={14} />{selected.credential_state === 'ready' ? '更新 MCP 凭据' : '录入 MCP 凭据'}</button> : <small>此 MCP 未声明 Secret 字段，不需要录入凭据。</small>}</div> : null}</section></div>
        {selected.status === 'quarantined' && view === 'project' ? <button className="primary-button resource-primary-action" disabled={review.isPending} onClick={() => review.mutate(selected)}><Check size={15} />审核并启用</button> : null}
        {review.error || repair.error || saveCredential.error || oauthStart.error || oauthStatus.error || oauthCancel.error || toggle.error || remove.error ? <p className="form-error">{review.error?.message ?? repair.error?.message ?? saveCredential.error?.message ?? oauthStart.error?.message ?? oauthStatus.error?.message ?? oauthCancel.error?.message ?? toggle.error?.message ?? remove.error?.message}</p> : null}
      </> : <div className="detail-empty"><PackageOpen size={28} /><h2>选择资源查看详情</h2></div>}</section>
    </div>
    {installType ? <ResourceInstallDialog projectId={resolvedProjectId} initialType={installType} onClose={() => setInstallType(undefined)} onInstalled={async (item, message) => { await refresh(); setView('project'); setKind(item.kind === 'skill' ? 'skill' : 'mcp'); setSelectedId(item.id); setNotice(message); setInstallType(undefined) }} /> : null}
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

type ResolvedMCP = { name: string; version: string; definition: MCPDefinitionV1 }

export function ResourceInstallDialog({ projectId, initialDraft, initialSource, initialType, onClose, onInstalled }: { projectId: string; initialDraft?: ResourceInstallDraft; initialSource?: string; initialType?: InstallType; onClose: () => void; onInstalled: (item: Capability, notice: string) => void | Promise<void> }) {
  const type = initialDraft?.kind === 'mcp' ? 'mcp' : initialType ?? 'skill'
  const fileRef = useRef<HTMLInputElement>(null)
  const credentialInputRefs = useRef<Record<string, HTMLInputElement | null>>({})
  const credentialBuffer = useRef<Record<string, string>>({})
  const [scope, setScope] = useState<CapabilityScope>('project')
  const [file, setFile] = useState<File>()
  const [source, setSource] = useState(initialSource ?? initialDraft?.source_input ?? '')
  const [draft, setDraft] = useState<ResourceInstallDraft | undefined>(initialDraft)
  const [resolved, setResolved] = useState<ResolvedMCP | undefined>(() => resolvedMCPFromDraft(initialDraft))
  const [manifest, setManifest] = useState(() => initialDraft?.mcp_definition ? JSON.stringify(initialDraft.mcp_definition, null, 2) : '')
  const [validated, setValidated] = useState(false)
  const [validationError, setValidationError] = useState('')
  const resolveDraft = useMutation({ mutationFn: () => api.resolveResourceInstallDraft(projectId, source), onSuccess: (result) => {
    setDraft(result)
    const next = resolvedMCPFromDraft(result)
    setResolved(next)
    setManifest(next ? JSON.stringify(next.definition, null, 2) : '')
    setValidated(false)
    setValidationError('')
  }, onError: (error) => setValidationError(error.message) })
  const selectCandidate = (candidate: MCPInstallCandidate) => {
    setResolved({ name: candidate.name, version: candidate.version, definition: candidate.mcp_definition })
    setManifest(JSON.stringify(candidate.mcp_definition, null, 2))
    setDraft((current) => current ? { ...current, resolved_name: candidate.name, resolved_version: candidate.version, mcp_definition: candidate.mcp_definition, candidates: undefined, required_secrets: candidate.mcp_definition.secret_bindings ?? [], status: candidate.mcp_definition.secret_bindings?.some((item) => item.required) ? 'needs_secret' : 'validated' } : current)
    setValidated(false)
    setValidationError('')
  }
  const validate = () => {
    install.reset()
    setValidationError('')
    if (type === 'skill') {
      if (!file || !file.name.toLowerCase().endsWith('.zip')) { setValidationError('Skill 只支持导入单 Skill ZIP。'); return }
      if (file.size > 50 * 1024 * 1024) { setValidationError('ZIP 超过 50 MiB 安全上限。'); return }
    } else {
      try { JSON.parse(manifest); if (!resolved) throw new Error('请先解析 MCP 来源。') } catch (error) { setValidationError(error instanceof Error ? error.message : 'MCP 配置必须是有效 JSON。'); return }
      const missing = (draft?.required_secrets ?? []).filter((binding) => binding.required && !credentialBuffer.current[binding.name]?.trim())
      if (missing.length) { setValidationError(`请补充必需凭据：${missing.map((binding) => binding.name).join('、')}`); return }
    }
    Object.values(credentialInputRefs.current).forEach((input) => { if (input) input.value = '' })
    setValidated(true)
  }
  const install = useMutation({ mutationFn: async () => {
    if (type === 'skill') return api.createCapability(projectId, { kind: 'skill', name: file!.name.replace(/\.zip$/i, ''), version: '0.1.0', manifest_json: '{}', scope_type: scope, source_filename: file!.name, bundle_base64: await fileToBase64(file!) })
    const credentials = Object.fromEntries(Object.entries(credentialBuffer.current).map(([name, value]) => [name, value.trim()]).filter(([, value]) => value))
    return api.createCapability(projectId, { kind: 'mcp', name: resolved!.name, version: resolved!.version, manifest_json: manifest, scope_type: scope, source_filename: resolved!.definition.source.reference || 'manual', credentials: Object.keys(credentials).length ? credentials : undefined })
  }, onSuccess: (item) => { credentialBuffer.current = {}; onInstalled(item, `“${item.name}”已进入统一安装链；当前状态：${availabilityText[item.availability] ?? item.status}。`) } })
  const repairSkillByConversation = () => {
    const problem = skillInstallErrorText(install.error?.message ?? validationError)
    window.sessionStorage.setItem(`oktapod.new-task-prefill.${projectId}`, `请使用内置 Skill Creator 修复我接下来附加的 Skill ZIP“${file?.name ?? '未命名 Skill'}”。资源中心完整校验未通过：${problem}请生成兼容的单 Skill ZIP，展示差异后重新走校验与审批链。`)
    window.location.assign(`/projects/${projectId}?conversation=new`)
  }

  return <div className="resource-install-overlay"><section className="resource-install-dialog resource-install-panel" role="dialog" aria-modal="true" aria-labelledby="resource-install-title">
    <header><div><span className="eyebrow">{type === 'skill' ? 'Skill ZIP' : 'MCP Definition v1'}</span><h2 id="resource-install-title">{type === 'skill' ? '导入 Skill' : '添加 MCP'}</h2><p>{type === 'skill' ? '只接受一个完整 Skill；不执行压缩包内容。' : '输入名称、URL、npm/PyPI 包或本地命令，系统会生成统一配置。'}</p></div><button type="button" aria-label="关闭安装窗口" onClick={onClose}><X size={18} /></button></header>
    <div className="resource-scope-choice"><button className={scope === 'project' ? 'active' : ''} onClick={() => { install.reset(); setScope('project'); setValidated(false) }}><strong>当前项目</strong><small>只对这个项目可用</small></button><button className={scope === 'global' ? 'active' : ''} onClick={() => { install.reset(); setScope('global'); setValidated(false) }}><strong>全局</strong><small>所有个人项目可用</small></button></div>
    {type === 'skill' ? <><button className="resource-file-drop" type="button" onClick={() => fileRef.current?.click()}><Upload size={20} /><span><strong>{file?.name || '选择单 Skill ZIP'}</strong><small>校验 SKILL.md、文件树、脚本风险和压缩安全</small></span></button><input ref={fileRef} hidden type="file" accept="application/zip,.zip" onChange={(event) => { install.reset(); setFile(event.target.files?.[0]); setValidated(false); setValidationError('') }} /><div className="resource-validation-rules"><span><Check size={13} />必须包含一份 SKILL.md</span><span><Check size={13} />允许 agents / scripts / references / assets</span><span><X size={13} />拒绝嵌套 Skill、node_modules、隐藏载荷和路径穿越</span></div></> : <>
      <label className="resource-source-input"><span>名称、URL、server.json、固定版本包或命令</span><div><input autoFocus value={source} onChange={(event) => { setSource(event.target.value); setDraft(undefined); setResolved(undefined); setValidated(false) }} placeholder="例如 Tavily、npm:@modelcontextprotocol/server-filesystem@1.0.0、https://…/mcp" /><button type="button" disabled={resolveDraft.isPending || !source.trim()} onClick={() => resolveDraft.mutate()}>{resolveDraft.isPending ? '查询中…' : '解析'}</button></div></label>
      {draft?.candidates?.length ? <div className="mcp-candidate-list" aria-label="MCP 来源候选"><strong>选择明确来源</strong>{draft.candidates.map((candidate) => <button type="button" key={`${candidate.name}@${candidate.version}`} onClick={() => selectCandidate(candidate)}><Cable size={15} /><span><b>{candidate.title || candidate.name}</b><small>{candidate.name} · v{candidate.version}{candidate.repository_url ? ` · ${candidate.repository_url.replace(/^https?:\/\//, '')}` : ''}</small></span></button>)}</div> : null}
      {resolved ? <div className="mcp-resolved-card"><span><CheckCircle2 size={16} /></span><div><strong>{resolved.name}</strong><small>{resolved.definition.transport.type === 'streamable_http' ? '远端 Streamable HTTP' : '本地 stdio'} · v{resolved.version}</small></div><em>已解析</em></div> : null}
      {draft?.warnings.map((warning) => <p className="resource-source-warning" key={warning}><CircleAlert size={14} />{warning}</p>)}
      {draft?.required_secrets?.length ? <div className="resource-secret-fields"><span><KeyRound size={14} />安全凭据 <em>仅写入 SecretStore；校验后立即清空输入框</em></span>{draft.required_secrets.map((binding) => <label className="resource-credential-field" key={`${binding.name}:${binding.target}`}><span>{binding.name}{binding.required ? <b>必需</b> : <em>可选</em>}<small>{binding.target.startsWith('env:') ? '本地环境变量' : '远端请求 Header'}</small></span><input ref={(input) => { credentialInputRefs.current[binding.name] = input }} type="password" autoComplete="new-password" onChange={(event) => { credentialBuffer.current[binding.name] = event.target.value; setValidated(false) }} placeholder="输入后不会回显" /></label>)}</div> : null}
      {resolved ? <details className="resource-advanced"><summary>高级配置</summary><textarea value={manifest} onChange={(event) => { setManifest(event.target.value); setValidated(false) }} rows={9} spellCheck={false} /></details> : null}
    </>}
    {validated && !install.error ? <div className="resource-install-validation"><strong>{type === 'skill' ? '客户端预检通过' : '格式预检通过'}</strong><span>{type === 'skill' ? '点击“执行完整校验”后，服务端才会扫描 SKILL.md、文件树、脚本风险和压缩安全。' : resolved?.definition.auth.type === 'oauth' ? '安装后在资源详情连接账号；授权码和令牌不会进入消息、manifest、日志或数据库业务字段。' : '安装前会固定来源和版本；Secret 不会进入消息、manifest、日志或数据库明文字段。'}</span></div> : null}
    {install.error && type === 'skill' ? <div className="resource-install-error" role="alert"><span><strong>完整校验未通过</strong><small>{skillInstallErrorText(install.error.message)}</small></span><button className="secondary-button" type="button" onClick={repairSkillByConversation}>用 Skill Creator 修复副本</button></div> : null}
    {validationError ? <p className="form-error">{validationError}</p> : null}{resolveDraft.error && !validationError ? <p className="form-error">{resolveDraft.error.message}</p> : null}{install.error && type !== 'skill' ? <p className="form-error">{install.error.message}</p> : null}
    <footer><span>安装、验证、审批、启用和调用使用同一资源生命周期。</span>{validated && !install.error ? <button className="primary-button" disabled={install.isPending} onClick={() => install.mutate()}>{install.isPending ? '正在校验…' : type === 'skill' ? '执行完整校验' : '确认安装'}</button> : <button className="primary-button" disabled={type === 'skill' ? !file : !resolved} onClick={validate}>{install.error && type === 'skill' ? '重新预检' : '校验配置'}</button>}</footer>
  </section></div>
}

function resolvedMCPFromDraft(draft?: ResourceInstallDraft): ResolvedMCP | undefined {
  if (!draft?.mcp_definition || !draft.resolved_name) return undefined
  return { name: draft.resolved_name, version: draft.resolved_version || 'resolved', definition: draft.mcp_definition }
}

function isOAuthMCP(item?: Capability) {
  if (!item || item.kind !== 'mcp') return false
  try { return (JSON.parse(item.manifest_json) as MCPDefinitionV1).auth?.type === 'oauth' } catch { return false }
}

function credentialStateText(item: Capability) {
  if (item.credential_state === 'missing') return '数据库中找不到该 credential_ref，请重新录入。'
  if (item.credential_state === 'locked') return '先使用恢复口令解锁数据库密钥库。'
  if (item.credential_state === 'corrupt') return '密文校验失败，禁止空值回退，请重新录入凭据。'
  return item.credential_configured ? '连接或健康检查未通过。' : '该资源没有可用凭据。'
}

const credentialStateLabel = { ready: '数据库密钥库 · 可用', locked: '密钥库锁定', missing: '凭据缺失', corrupt: '密文损坏' } as const

function fileToBase64(file: File) {
  return new Promise<string>((resolve, reject) => { const reader = new FileReader(); reader.onerror = () => reject(new Error('无法读取所选 ZIP。')); reader.onload = () => resolve(String(reader.result).split(',', 2)[1] || ''); reader.readAsDataURL(file) })
}

function skillInstallErrorText(message: string) {
  const detail = message.replace(/^bundle_base64:\s*/i, '').trim()
  const unsupportedRoot = detail.match(/unsupported root file:\s*(.+)$/i)
  if (unsupportedRoot) return `根目录文件“${unsupportedRoot[1]}”不受支持；根目录只允许 SKILL.md 和许可证文件。`
  if (/frontmatter/i.test(detail)) return `SKILL.md frontmatter 不兼容：${detail}`
  if (/nested skill/i.test(detail)) return 'ZIP 中包含嵌套 Skill；请拆分为每个 ZIP 仅一份 SKILL.md。'
  if (/node_modules/i.test(detail)) return 'ZIP 包含 node_modules；请删除依赖目录，只保留 Skill 源文件。'
  return detail || 'ZIP 不符合单 Skill 安全格式。'
}
