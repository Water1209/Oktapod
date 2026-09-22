import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { useSearch } from '@tanstack/react-router'
import { ArrowLeft, Bot, Braces, Cable, Check, CheckCircle2, ChevronDown, CircleAlert, Code2, KeyRound, MoreHorizontal, PackageOpen, Plus, Search, ShieldCheck, Trash2, Upload, Wrench, X } from 'lucide-react'
import { api, type Capability, type MCPDefinitionV1, type MCPOAuthSession, type PresetResourceCatalogItem, type RuntimeDependency } from '../../shared/api/client'
import { mcpSecretBindings } from './mcp-secret-bindings'
import { filterProjectResources, filterSystemResources, resourceEmptyCopy, resourceKindCounts, type ResourceKind, type ResourceView } from './resource-filter'
import { availabilityText, resourceKindText, resourceProfile, systemResourceProfiles, type ResourceItem } from './resource-profile'
import { RuntimeRepairGuide } from '../diagnostics/runtime-repair-guide'
import { ResourceInstallDialog, type InstallType } from './resource-install-dialog'


const systemResources = Object.keys(systemResourceProfiles).map((name) => {
  const isSkill = name === 'Skill Creator'
  return {
    id: isSkill ? 'builtin-skill-creator' : `builtin-tool-${name}`,
    kind: isSkill ? 'skill' : 'plugin',
    name,
    version: 'builtin',
    status: 'enabled',
    availability: 'available',
    risk_level: isSkill ? 'L0' : name === 'run_command' ? 'L2' : 'L0/L1',
    source_filename: isSkill ? 'Oktapod System' : 'Oktapod Core',
    scope_type: 'global',
    manifest_json: '',
    permission_diff: isSkill ? [] : name === 'run_command' ? ['command'] : ['workspace'],
    content_hash: isSkill ? 'system:skill-creator' : 'system:tool',
    origin: 'system',
    dependencies: [],
    update_state: 'current',
    description: systemResourceProfiles[name]?.summary || '',
  }
}) as unknown as ResourceItem[]


export function ResourcesPage() {
  const queryClient = useQueryClient()
  const search = useSearch({ from: '/resources' })
  const projects = useQuery({ queryKey: ['projects'], queryFn: api.projects })
  const [projectId, setProjectId] = useState('')
  const resolvedProjectId = projectId || search.project || projects.data?.items[0]?.id || ''
  const capabilities = useQuery({ queryKey: ['capabilities', resolvedProjectId], queryFn: () => api.capabilities(resolvedProjectId), enabled: Boolean(resolvedProjectId) })
  const catalog = useQuery({ queryKey: ['resource-catalog', resolvedProjectId], queryFn: () => api.resourceCatalog(resolvedProjectId), enabled: Boolean(resolvedProjectId) })
  const agents = useQuery({ queryKey: ['agents', resolvedProjectId], queryFn: () => api.agents(resolvedProjectId), enabled: Boolean(resolvedProjectId) })
  const installed = useMemo<ResourceItem[]>(() => [
    ...(capabilities.data?.items ?? []),
    ...(agents.data?.items ?? []).map((agent) => ({ ...agent, kind: 'agent', availability: agent.status === 'enabled' ? 'available' : agent.status, risk_level: 'L0', source_filename: '项目 Agent 定义', scope_type: 'project', manifest_json: agent.config_json, permission_diff: [], content_hash: `agent:${agent.id}`, origin: 'custom', dependencies: [], update_state: 'current', description: agent.instructions } as unknown as ResourceItem)),
    ...(catalog.data?.items ?? []).filter((item) => item.install_state === 'available').map(catalogResourceItem),
  ] satisfies ResourceItem[], [agents.data, capabilities.data, catalog.data])
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
  const [runtimeDependencyInstallId, setRuntimeDependencyInstallId] = useState('')
  const repairCredentialInputRefs = useRef<Record<string, HTMLInputElement | null>>({})
  const repairCredentialBuffer = useRef<Record<string, string>>({})
  const oauthPopup = useRef<Window | null>(null)
  const counts = useMemo(() => resourceKindCounts(installed), [installed])
  const filtered = view === 'project' ? filterProjectResources(installed, kind, query) : filterSystemResources(systemResources, query)
  const selected = filtered.find((item) => item.id === selectedId) ?? filtered[0]
  const selectedIsCatalog = selected?.catalog_install_state === 'available'
  const selectedIsDocumentRuntime = selected?.preset_key === 'document-runtime'
  const dependentResources = selectedIsDocumentRuntime
    ? (capabilities.data?.items ?? []).filter((item) => item.dependencies.includes('document.runtime'))
    : []
  const emptyCopy = resourceEmptyCopy(view, kind, Boolean(query.trim()))
  const selectedUsesOAuth = isOAuthMCP(selected)
  const selectedSecretBindings = mcpSecretBindings(selected)
  const health = useQuery({ queryKey: ['mcp-health', selected?.id], queryFn: () => api.mcpHealth(selected!.id), enabled: Boolean(selected?.kind === 'mcp' && selected.status === 'enabled'), retry: false })
  const enabledMCPs = useMemo(() => (capabilities.data?.items ?? []).filter((item) => item.kind === 'mcp' && item.status === 'enabled'), [capabilities.data])
  const runtimeDependencyQueries = useQueries({ queries: enabledMCPs.map((item) => ({ queryKey: ['runtime-dependencies', item.id], queryFn: () => api.runtimeDependencies(item.id), enabled: true, retry: false })) })
  const runtimeDependenciesByID = useMemo(() => {
    const result = new Map<string, RuntimeDependency[]>()
    enabledMCPs.forEach((item, index) => result.set(item.id, runtimeDependencyQueries[index]?.data?.items ?? []))
    return result
  }, [enabledMCPs, runtimeDependencyQueries])
  const selectedRuntimeDependencies = selected ? runtimeDependenciesByID.get(selected.id) ?? [] : []
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
    queryClient.invalidateQueries({ queryKey: ['resource-catalog', resolvedProjectId] }),
    queryClient.invalidateQueries({ queryKey: ['agents', resolvedProjectId] }),
  ])
  const review = useMutation({ mutationFn: async (item: Capability) => {
    const approved = await api.reviewCapability(item.id, 'approved', '用户在资源中心核对来源、作用域、权限与凭据状态后批准')
    if (item.kind === 'mcp' && !isOAuthMCP(item)) await api.connectMCP(item.id)
    return approved
  }, onSuccess: (item) => {
    setNotice(item.kind === 'mcp' ? `“${item.name}”已审核并启用，连接验证通过。` : `“${item.name}”已审核并启用。`)
  }, onSettled: async () => { await refresh(); await queryClient.invalidateQueries({ queryKey: ['mcp-health', selected?.id] }) } })
  const repair = useMutation({ mutationFn: (item: Capability) => api.connectMCP(item.id), onSettled: async () => { await refresh(); await queryClient.invalidateQueries({ queryKey: ['mcp-health', selected?.id] }) } })
  const installRuntimeDependency = useMutation({ mutationFn: ({ item, dependency }: { item: Capability; dependency: RuntimeDependency }) => api.installRuntimeDependency(item.id, dependency.provider, dependency.id), onSuccess: async () => {
    setRuntimeDependencyInstallId('')
    setNotice('运行时依赖已安装，MCP 已自动重新验证。')
    await refresh()
    await queryClient.invalidateQueries({ queryKey: ['mcp-health', selected?.id] })
    await queryClient.invalidateQueries({ queryKey: ['runtime-dependencies'] })
  }, onSettled: async () => { await queryClient.invalidateQueries({ queryKey: ['runtime-dependencies'] }) } })
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
  const remove = useMutation({ mutationFn: async ({ item, cascade }: { item: Capability; cascade: boolean }) => {
    if (cascade && item.preset_key === 'document-runtime') {
      for (const dependent of dependentResources) await api.deleteCapability(dependent.id)
    }
    return item.kind === 'agent' ? api.deleteAgent(item.id) : api.deleteCapability(item.id)
  }, onSuccess: async () => { setSelectedId(undefined); setConfirmDelete(false); setNotice('资源已删除；预置资源保持删除状态，历史 Run 快照和脱敏审计仍保留。'); await refresh() } })
  const installPreset = useMutation({ mutationFn: (item: ResourceItem) => api.installPresetResource(resolvedProjectId, item.preset_key!), onSuccess: async (item) => { await refresh(); setSelectedId(item.id); setNotice(`“${item.name}”已从预置目录安装；后续升级会保留你的启用状态。`) } })
  const oauthStart = useMutation({ mutationFn: (item: Capability) => api.startMCPOAuth(item.id), onSuccess: (session) => {
    setOAuthSession(session)
    if (session.authorization_url && oauthPopup.current) oauthPopup.current.location.assign(session.authorization_url)
    if (session.status !== 'preparing' && session.status !== 'waiting') oauthPopup.current?.close()
  }, onError: () => { oauthPopup.current?.close(); oauthPopup.current = null } })
  const oauthCancel = useMutation({ mutationFn: ({ item, session }: { item: Capability; session: MCPOAuthSession }) => api.cancelMCPOAuth(item.id, session.id), onSuccess: (session) => setOAuthSession(session) })
  const activeOAuthSession = oauthStatus.data ?? oauthSession
  const selectedProfile = selected ? resourceProfile(selected, health.data?.tools_json) : undefined
  const selectedDisplayName = selected ? selectedProfile?.displayName || selected.name : ''
  const selectedRuntimeDegraded = Boolean(selected?.preset_key && selected.availability === 'degraded')
  const selectedAllowsRuntimeDependencyCheck = selected?.kind === 'mcp' && selected.status === 'enabled'
  const selectedHasRuntimeDependencyIssue = selectedAllowsRuntimeDependencyCheck && selectedRuntimeDependencies.length > 0
  const selectedHasRepairIssue = selectedHasRuntimeDependencyIssue || selectedRuntimeDegraded || Boolean(health.error || health.data?.last_error || selected?.credential_state === 'missing' || selected?.credential_state === 'locked' || selected?.credential_state === 'corrupt')
  const selectedAvailability = selectedHasRuntimeDependencyIssue ? 'degraded' : (selected?.availability ?? (selected?.status === 'enabled' ? 'available' : selected?.status ?? 'disabled'))

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
        {filtered.map((item) => { const profile = resourceProfile(item); const hasRuntimeDependencyIssue = item.kind === 'mcp' && Boolean(runtimeDependenciesByID.get(item.id)?.length); const itemAvailability = hasRuntimeDependencyIssue ? 'degraded' : (item.availability ?? (item.status === 'enabled' ? 'available' : item.status)); return <button type="button" className={item.id === selected?.id ? 'active' : ''} key={item.id} onClick={() => { if (item.id !== selected?.id) { oauthPopup.current?.close(); oauthPopup.current = null; setOAuthSession(undefined) } setSelectedId(item.id) }}><ResourceIcon item={item} /><span><strong>{profile.displayName || item.name}</strong><small>{resourceOriginText(item)} · {item.kind === 'plugin' ? 'Plugin' : item.kind}</small><small className="resource-list-description">{profile.summary}</small></span><em className={item.catalog_install_state ? 'catalog' : itemAvailability}><i />{item.catalog_install_state ? '可安装' : availabilityText[itemAvailability as keyof typeof availabilityText] ?? item.status}</em></button> })}
        {!filtered.length ? <div className="master-empty"><PackageOpen size={22} /><strong>{emptyCopy.title}</strong><span>{emptyCopy.detail}</span>{view === 'project' && kind !== 'all' && !query.trim() ? <button type="button" onClick={() => setKind('all')}>显示全部项目资源</button> : null}</div> : null}
      </section>
      <section className="resource-detail-v2">{selected ? <>
        <button className="resource-mobile-back" type="button" onClick={() => setSelectedId(undefined)}><ArrowLeft size={16} />返回列表</button>
        <header><ResourceIcon item={selected} /><div><span>{resourceOriginText(selected)} · {selected.kind === 'plugin' ? 'PLUGIN' : selected.kind.toUpperCase()}</span><h2>{selectedDisplayName}</h2><p>{selectedDisplayName !== selected.name ? selected.name : selected.source_filename || '项目定义'} · v{selected.version}</p></div>{view === 'project' && !selectedIsCatalog ? <div className="resource-detail-actions"><label className="resource-toggle"><input type="checkbox" checked={selected.status === 'enabled'} disabled={selected.status === 'quarantined' || toggle.isPending} onChange={() => toggle.mutate(selected)} /><span /></label><button aria-label="更多操作" onClick={() => setConfirmDelete((value) => !value)}><MoreHorizontal size={17} /></button></div> : null}</header>
        {selectedIsCatalog ? <CatalogLifecycle /> : <Lifecycle availability={selectedAvailability} />}
        {confirmDelete ? <div className="resource-delete-confirm" role="alert"><Trash2 size={17} /><span><strong>{dependentResources.length ? `删除 Runtime 及 ${dependentResources.length} 个依赖资源？` : '永久删除此资源？'}</strong><small>{dependentResources.length ? `将级联删除：${dependentResources.map((item) => item.name).join('、')}。预置资源不会在重启或升级时静默恢复。` : '活动任务正在引用时会被阻止；预置资源不会在重启或升级时静默恢复，历史快照和脱敏审计不会删除。'}</small></span><button type="button" onClick={() => setConfirmDelete(false)}>取消</button><button className="danger" type="button" disabled={remove.isPending} onClick={() => remove.mutate({ item: selected, cascade: dependentResources.length > 0 })}>{dependentResources.length ? '确认级联删除' : '确认删除'}</button></div> : null}
        {selectedHasRepairIssue ? <><div className="resource-repair"><CircleAlert size={17} /><span><strong>{selectedHasRuntimeDependencyIssue ? '缺少运行时依赖' : selectedUsesOAuth ? '需要连接账号' : selectedRuntimeDegraded ? '主机文档 Runtime 未就绪' : selected?.credential_state === 'missing' ? '凭据缺失' : selected?.credential_state === 'locked' ? '密钥库已锁定' : selected?.credential_state === 'corrupt' ? '凭据密文损坏' : (health.error as { problem?: { code?: string } } | undefined)?.problem?.code === 'MCP_NOT_CONNECTED' ? '尚未连接' : '需要修复'}</strong><small>{selectedHasRuntimeDependencyIssue ? selectedRuntimeDependencies[0]?.description : selectedUsesOAuth ? (activeOAuthSession?.last_error || '通过服务商授权页连接账号；令牌只保存在数据库密钥库。') : selectedRuntimeDegraded ? '资源目录已安装，但当前进程没有加载可用的签名 document-runtime。先重新验证；若文件损坏或误删，请按下方步骤替换完整 Runtime 包。' : ((health.error as { problem?: { code?: string } } | undefined)?.problem?.code === 'MCP_NOT_CONNECTED' ? '资源已启用，但还没有建立连接记录；点击重新验证即可。' : (health.error as { message?: string } | undefined)?.message || health.data?.last_error || credentialStateText(selected))}</small></span>{selected.preset_key ? <button className="primary-button" disabled={installPreset.isPending} onClick={() => installPreset.mutate(selected)}><Wrench size={14} />验证运行环境</button> : selected.kind === 'mcp' ? selectedUsesOAuth ? <button className="primary-button" disabled={oauthStart.isPending || activeOAuthSession?.status === 'waiting'} onClick={() => beginOAuth(selected)}><KeyRound size={14} />连接账号</button> : selected.credential_state === 'missing' || selected.credential_state === 'corrupt' ? <button className="primary-button" onClick={() => setCredentialEditorId(selected.id)}><KeyRound size={14} />重新录入</button> : selected.credential_state === 'locked' ? <a className="primary-button" href="/settings/vault"><KeyRound size={14} />恢复 Vault</a> : <button className="primary-button" disabled={repair.isPending} onClick={() => repair.mutate(selected)}><Wrench size={14} />重新验证</button> : null}</div>{selectedRuntimeDependencies.length ? <div className="resource-runtime-dependencies" role="region" aria-label="运行时依赖">{selectedRuntimeDependencies.map((dependency) => <div key={`${dependency.provider}:${dependency.id}`}><span><strong>{dependency.display_name}</strong><small>{dependency.description}</small></span><button className="primary-button" type="button" disabled={!dependency.installable || installRuntimeDependency.isPending || runtimeDependencyInstallId === dependency.id} onClick={() => setRuntimeDependencyInstallId(dependency.id)}>{dependency.installable ? '确认安装' : '无法自动安装'}</button></div>)}</div> : null}{selectedRuntimeDependencies.some((dependency) => dependency.id === runtimeDependencyInstallId) ? <div className="resource-delete-confirm" role="alert"><Trash2 size={17} /><span><strong>确认下载并安装运行时依赖？</strong><small>{selectedRuntimeDependencies.find((dependency) => dependency.id === runtimeDependencyInstallId)?.description} 安装会写入 Oktapod 隔离缓存，完成后自动重新验证 MCP。</small></span><button type="button" onClick={() => setRuntimeDependencyInstallId('')}>取消</button><button type="button" disabled={installRuntimeDependency.isPending} onClick={() => installRuntimeDependency.mutate({ item: selected, dependency: selectedRuntimeDependencies.find((dependency) => dependency.id === runtimeDependencyInstallId)! })}>{installRuntimeDependency.isPending ? '正在安装…' : '同意并安装'}</button></div> : null}{selectedRuntimeDegraded ? <RuntimeRepairGuide detail={(health.error as { message?: string } | undefined)?.message} /> : null}</> : null}
        {selected.kind === 'mcp' && !selectedUsesOAuth && credentialEditorId === selected.id ? <form className="resource-credential-reentry" onSubmit={(event) => { event.preventDefault(); saveCredential.mutate(selected) }}><header><div><strong>重新录入 MCP 凭据</strong><small>值只发送到数据库密钥库，不会写入 manifest、页面存储或日志。</small></div><button type="button" aria-label="关闭凭据录入" onClick={() => setCredentialEditorId('')}><X size={14} /></button></header>{selectedSecretBindings.map((binding) => <label className="resource-credential-field" key={`${binding.name}:${binding.target}`}><span>{binding.name}{binding.required ? <b>必需</b> : <em>可选</em>}<small>{binding.target.startsWith('env:') ? '注入本地 MCP 进程' : '注入远端请求 Header'}</small></span><input ref={(input) => { repairCredentialInputRefs.current[binding.name] = input }} required={binding.required} type="password" autoComplete="new-password" placeholder="输入新凭据" onChange={(event) => { repairCredentialBuffer.current[binding.name] = event.target.value }} /></label>)}<footer><button className="secondary-button compact" type="button" onClick={() => setCredentialEditorId('')}>取消</button><button className="primary-button compact" type="submit" disabled={saveCredential.isPending || selectedSecretBindings.length === 0}>{saveCredential.isPending ? '保存并验证中…' : '保存并测试连接'}</button></footer></form> : null}
        {selectedUsesOAuth && activeOAuthSession?.capability_id === selected.id && (activeOAuthSession.status === 'preparing' || activeOAuthSession.status === 'waiting') ? <div className="resource-oauth-session" role="status"><span><strong>等待浏览器授权</strong><small>授权完成后此处会自动验证 MCP；关闭授权页不会删除资源。</small></span>{activeOAuthSession.authorization_url ? <a href={activeOAuthSession.authorization_url} target="_blank" rel="noreferrer">打开授权页</a> : null}<button type="button" disabled={oauthCancel.isPending} onClick={() => oauthCancel.mutate({ item: selected, session: activeOAuthSession })}>取消授权</button></div> : null}
        {selectedProfile ? <div className="resource-capability-anatomy">
          <section className="resource-purpose-card"><span className="resource-section-index">01</span><div><small>能力定位</small><h3>它能做什么</h3><p>{selectedProfile.summary}</p></div><dl><div><dt>类型</dt><dd>{resourceKindText(selected.kind)}</dd></div><div><dt>作用域</dt><dd>{selected.scope_type === 'global' ? '所有个人项目' : '仅当前项目'}</dd></div>{health.data ? <div><dt>在线工具</dt><dd>{health.data.tool_count} 个</dd></div> : null}</dl></section>
          <div className="resource-insight-grid"><section><header><span className="resource-section-index">02</span><div><small>能力明细</small><h3>{selectedProfile.abilityHeading}</h3></div></header><ul className="resource-ability-list">{selectedProfile.abilities.map((ability) => <li key={`${ability.title}:${ability.detail}`}><strong>{ability.title}</strong>{ability.technicalName ? <code>{ability.technicalName}</code> : null}<small>{ability.detail}</small></li>)}</ul>{selected.kind === 'mcp' && health.data && health.data.tool_count > selectedProfile.abilities.length ? <p className="resource-tool-summary">当前连接共提供 <strong>{health.data.tool_count}</strong> 个工具；上方按用户任务归纳核心能力。</p> : null}</section><section><header><span className="resource-section-index">03</span><div><small>调用判断</small><h3>适合这些任务</h3></div></header><ul className="resource-use-case-list">{selectedProfile.useCases.map((useCase) => <li key={useCase}>{useCase}</li>)}</ul></section></div>
          <section className="resource-usage-card"><header><span className="resource-section-index">04</span><div><small>对话入口</small><h3>怎么使用</h3></div></header><ol>{selectedProfile.usage.map((item) => <li key={item}>{item}</li>)}</ol></section>
          <section className="resource-boundary-card"><header><span className="resource-section-index">05</span><div><small>治理边界</small><h3>权限与运行条件</h3></div></header><div className="resource-boundary-grid"><div className="trust-note"><ShieldCheck size={17} /><span><strong>{selected.risk_level || (selected.preset_key ? '固定策略' : 'L0')} 风险级别</strong><small>{selected.permission_diff?.length ? `声明权限：${selected.permission_diff.join('、')}。每次调用仍由策略、Authorization 与 Receipt 检查。` : selected.preset_key ? 'Skill 只声明工作流；document_* 调用继续经过 ToolRuntime、策略、Receipt 与 ArtifactCapture。' : '未声明额外扩权；运行时策略仍逐次检查。'}</small></span></div><dl><div><dt>来源</dt><dd>{resourceOriginText(selected)}</dd></div><div><dt>依赖</dt><dd>{selected.dependencies?.length ? selected.dependencies.join('、') : '无'}</dd></div><div><dt>{selectedUsesOAuth ? '账号' : '凭据'}</dt><dd>{selected.kind === 'mcp' ? selectedUsesOAuth && selected.credential_state === 'ready' ? 'OAuth 令牌可用' : credentialStateLabel[selected.credential_state] : '不需要'}</dd></div>{!selectedIsCatalog ? <div><dt>内容标识</dt><dd><code>{selected.content_hash}</code></dd></div> : null}</dl></div>{selected.kind === 'mcp' ? <div className="resource-credential-actions">{selectedUsesOAuth ? <button className="secondary-button compact" type="button" disabled={oauthStart.isPending} onClick={() => beginOAuth(selected)}><KeyRound size={14} />{selected.credential_state === 'ready' ? '重新连接账号' : '连接账号'}</button> : selectedSecretBindings.length ? <button className="secondary-button compact" type="button" onClick={() => setCredentialEditorId(selected.id)}><KeyRound size={14} />{selected.credential_state === 'ready' ? '更新 MCP 凭据' : '录入 MCP 凭据'}</button> : <small>此 MCP 未声明 Secret 字段，不需要录入凭据。</small>}</div> : null}</section>
        </div> : null}
        {selectedIsCatalog ? <button className="primary-button resource-primary-action" disabled={installPreset.isPending} onClick={() => installPreset.mutate(selected)}><Plus size={15} />安装预置资源</button> : null}
        {selected.status === 'quarantined' && view === 'project' ? <button className="primary-button resource-primary-action" disabled={review.isPending} onClick={() => review.mutate(selected)}><Check size={15} />审核并启用</button> : null}
        {review.error || repair.error || saveCredential.error || oauthStart.error || oauthStatus.error || oauthCancel.error || toggle.error || remove.error || installPreset.error || installRuntimeDependency.error ? <p className="form-error">{review.error?.message ?? repair.error?.message ?? saveCredential.error?.message ?? oauthStart.error?.message ?? oauthStatus.error?.message ?? oauthCancel.error?.message ?? toggle.error?.message ?? remove.error?.message ?? installPreset.error?.message ?? installRuntimeDependency.error?.message}</p> : null}
      </> : <div className="detail-empty"><PackageOpen size={28} /><h2>选择资源查看详情</h2></div>}</section>
    </div>
    {installType ? <ResourceInstallDialog projectId={resolvedProjectId} initialType={installType} onClose={() => setInstallType(undefined)} onInstalled={async (item, message) => { await refresh(); setView('project'); setKind(item.kind === 'skill' ? 'skill' : 'mcp'); setSelectedId(item.id); setNotice(message); setInstallType(undefined) }} /> : null}
  </main>
}

function ResourceIcon({ item }: { item: Capability }) {
  const Icon = item.kind === 'mcp' ? Cable : item.kind === 'agent' ? Bot : item.kind === 'plugin' ? Code2 : Braces
  return <span className={`resource-kind ${item.kind}`}><Icon size={16} /></span>
}

function catalogResourceItem(item: PresetResourceCatalogItem): ResourceItem {
  return {
    id: `catalog:${item.preset_key}`,
    organization_id: '', project_id: '', owner_user_id: '', kind: item.kind, name: item.name,
    version: item.version, manifest_json: '', content_hash: '', status: 'disabled', availability: item.availability,
    risk_level: item.kind === 'plugin' ? 'L0/L1' : 'L0', permission_diff: [], scope_type: 'global', scope_id: '',
    source_filename: 'Oktapod Preset Catalog', bundle_hash: '', install_job_id: '', credential_configured: false,
    credential_state: 'ready', origin: 'preset', preset_key: item.preset_key, dependencies: item.dependencies,
    managed_version: item.managed_version, update_state: item.update_state, created_at: '', updated_at: '',
    catalog_install_state: 'available', description: item.description,
  }
}

function resourceOriginText(item: Capability) {
  if (item.origin === 'preset') return '预置资源'
  if (item.origin === 'system') return '系统能力'
  return '自定义资源'
}

function CatalogLifecycle() {
  return <div className="resource-lifecycle catalog" aria-label="预置资源目录状态"><span className="done"><i><Check size={12} /></i><strong>已签名</strong></span><b /><span className="done"><i><Check size={12} /></i><strong>可安装</strong></span><b /><span><i>3</i><strong>未安装</strong></span></div>
}

function Lifecycle({ availability }: { availability: string }) {
  const validated = availability !== 'quarantined'
  const available = availability === 'available'
  const finalLabel = availability === 'degraded' ? '需要修复' : availability === 'disabled' ? '已停用' : availability === 'quarantined' ? '待审核' : '可用'
  return <div className="resource-lifecycle" aria-label="资源生命周期"><span className="done"><i><Check size={12} /></i><strong>已安装</strong></span><b /><span className={validated ? 'done' : ''}><i>{validated ? <Check size={12} /> : '2'}</i><strong>{availability === 'quarantined' ? '待验证' : '已验证'}</strong></span><b /><span className={available ? 'done' : availability === 'degraded' ? 'warning' : ''}><i>{available ? <Check size={12} /> : '3'}</i><strong>{finalLabel}</strong></span></div>
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
