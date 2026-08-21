import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, Navigate, useParams } from '@tanstack/react-router'
import { AlertTriangle, BadgeDollarSign, Bot, CheckCircle2, Copy, Database, Download, Globe2, HardDrive, KeyRound, Palette, Plus, RotateCcw, Save, Server, ShieldCheck, SlidersHorizontal, Trash2 } from 'lucide-react'
import { api, displayError, type AIGovernanceSettings, type BootstrapConfiguration, type ContextSettings, type ModelPricingVersion, type ProviderInstance, type UserPreferences } from '../../shared/api/client'
import { applyPresentationPreferences, defaultPresentationPreferences, presentationPreferenceStorageKey, readPresentationPreferences } from '../../shared/presentation-preferences'
import { validatePrivateFetchDraft } from './private-fetch-validation'

type SettingsSection = 'governance' | 'runtime' | 'pricing' | 'vault' | 'data' | 'appearance'
type SettingsNavigation = 'providers' | SettingsSection

export function SettingsPage() {
  const { section } = useParams({ from: '/settings/$section' })
  if (!isSettingsSection(section)) return <Navigate to="/settings/$section" params={{ section: 'runtime' }} />
  if (section === 'governance') return <GovernanceSettings />
  if (section === 'runtime') return <RuntimeSettings />
  if (section === 'pricing') return <PricingSettings />
  if (section === 'vault') return <VaultSettings />
  if (section === 'data') return <DataSettings />
  return <AppearanceSettings />
}

function RuntimeSettings() {
  const queryClient = useQueryClient()
  const configuration = useQuery({ queryKey: ['configuration'], queryFn: api.configuration })
  const context = useQuery({ queryKey: ['context-settings'], queryFn: api.contextSettings })
  const [bootstrapDraft, setBootstrapDraft] = useState<Partial<BootstrapConfiguration>>({})
  const [contextDraft, setContextDraft] = useState<Partial<ContextSettings>>({})
  const [savedBootstrap, setSavedBootstrap] = useState<BootstrapConfiguration>()
  const bootstrapBase = savedBootstrap ?? configuration.data
  const effectiveBootstrap = bootstrapBase ? { ...bootstrapBase, ...bootstrapDraft } : undefined
  const effectiveContext = context.data ? { ...context.data, ...contextDraft } : undefined
  const privateFetch = effectiveBootstrap?.web_access?.private_fetch
  const privateFetchValidationError = validatePrivateFetchDraft(privateFetch)
  const privateFetchInvalid = Boolean(privateFetchValidationError)
  const hasBootstrapChanges = Object.keys(bootstrapDraft).length > 0
  const hasContextChanges = Object.keys(contextDraft).length > 0
  const saveAll = useMutation({
    mutationFn: async () => {
      const { web_access: webAccessDraft, ...bootstrapFields } = bootstrapDraft
      const bootstrapInput = webAccessDraft ? {
        ...bootstrapFields,
        web_access: { private_fetch: { enabled: webAccessDraft.private_fetch.enabled, allowed_origins: webAccessDraft.private_fetch.allowed_origins } },
      } : bootstrapFields
      const bootstrap = hasBootstrapChanges ? await api.updateBootstrapConfiguration(bootstrapInput) : undefined
      const contextSettings = hasContextChanges ? await api.updateContextSettings({
        auto_compact: effectiveContext?.auto_compact ?? true,
        trigger_tokens: effectiveContext?.trigger_tokens ?? 80_000,
        retain_tokens: effectiveContext?.retain_tokens ?? 20_000,
        summary_max_tokens: effectiveContext?.summary_max_tokens ?? 4_000,
      }) : undefined
      return { bootstrap, contextSettings }
    },
    onSuccess: async ({ bootstrap }) => {
      if (bootstrap) setSavedBootstrap(bootstrap)
      setBootstrapDraft({})
      setContextDraft({})
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['configuration'] }),
        queryClient.invalidateQueries({ queryKey: ['context-settings'] }),
        queryClient.invalidateQueries({ queryKey: ['diagnostics'] }),
      ])
    },
  })
  const updatePrivateFetch = (next: Partial<BootstrapConfiguration['web_access']['private_fetch']>) => {
    if (!effectiveBootstrap) return
    setBootstrapDraft({
      ...bootstrapDraft,
      web_access: { private_fetch: { ...effectiveBootstrap.web_access.private_fetch, ...next } },
    })
  }

  return <SettingsLayout active="runtime">
    <div className="settings-group settings-ledger-page">
      <SettingsPageHeader icon={<Database />} title="运行与上下文" description="控制服务如何启动，以及长会话何时压缩。价格与治理策略已拆到各自页面。" scope="服务 + 组织" />
      {effectiveBootstrap ? <>
        <ConfigSourceCard configuration={savedBootstrap ?? configuration.data!} saved={Boolean(savedBootstrap)} />
        <SettingsSectionHeader title="服务启动" description="写入 config.yaml，保存后需重启服务才会使用新值。" />
        <div className="settings-form-grid">
          <SettingRow label="监听地址" meta={sourceMeta(configuration.data!, true)}><input value={effectiveBootstrap.listen_address} onChange={(event) => setBootstrapDraft({ ...bootstrapDraft, listen_address: event.target.value })} /></SettingRow>
          <SettingRow label="Data Root" meta={sourceMeta(configuration.data!, true)}><input value={effectiveBootstrap.data_root} placeholder="留空使用默认路径" onChange={(event) => setBootstrapDraft({ ...bootstrapDraft, data_root: event.target.value })} /></SettingRow>
          <SettingRow label="并发 Run" meta={sourceMeta(configuration.data!, true)}><input type="number" min={1} max={4} value={effectiveBootstrap.run_concurrency} onChange={(event) => setBootstrapDraft({ ...bootstrapDraft, run_concurrency: Number(event.target.value) })} /></SettingRow>
          <SettingRow label="Capability 信任公钥文件" meta={sourceMeta(configuration.data!, true)}><input value={effectiveBootstrap.trusted_keys_file} onChange={(event) => setBootstrapDraft({ ...bootstrapDraft, trusted_keys_file: event.target.value })} /></SettingRow>
        </div>
        <SettingsSectionHeader title="Web Access 网络边界" description="公网网页保持受控读取；私网仅允许精确 IP + 端口，保存后重启生效。" />
        {privateFetch ? <section className={`private-fetch-settings ${privateFetch.enabled ? 'enabled' : ''}`}>
          <div className="private-fetch-heading"><span><Globe2 size={18} /><span><strong>受控私网读取</strong><small>首次访问每个 origin 仍需人工批准；完全访问和 AI Reviewer 不会代替该审批。</small></span></span><label><input aria-label="启用受控私网读取" type="checkbox" disabled={privateFetch.origins_redacted} checked={privateFetch.enabled} onChange={(event) => updatePrivateFetch({ enabled: event.target.checked })} />{privateFetch.enabled ? '已启用' : '默认关闭'}</label></div>
          {privateFetch.origins_redacted ? <div className="private-fetch-redacted"><ShieldCheck size={16} /><span>当前配置包含 {privateFetch.allowed_origin_count} 条规则。只有 Owner / Admin 可以查看完整 origin 并修改。</span></div> : <div className="private-origin-editor">
            {privateFetch.allowed_origins.map((item, index) => <div className="private-origin-row" key={`${item.id}:${index}`}><input aria-label={`规则 ${index + 1} ID`} value={item.id} placeholder="intranet-wiki" onChange={(event) => updatePrivateFetch({ allowed_origins: privateFetch.allowed_origins.map((origin, row) => row === index ? { ...origin, id: event.target.value } : origin) })} /><input aria-label={`规则 ${index + 1} Origin`} value={item.origin} placeholder="http://192.168.10.20:8080" onChange={(event) => updatePrivateFetch({ allowed_origins: privateFetch.allowed_origins.map((origin, row) => row === index ? { ...origin, origin: event.target.value } : origin) })} /><button type="button" aria-label={`删除规则 ${item.id || index + 1}`} onClick={() => updatePrivateFetch({ allowed_origins: privateFetch.allowed_origins.filter((_, row) => row !== index) })}><Trash2 size={14} /></button></div>)}
            <button className="secondary-button compact private-origin-add" type="button" disabled={privateFetch.allowed_origins.length >= 64} onClick={() => updatePrivateFetch({ allowed_origins: [...privateFetch.allowed_origins, { id: '', origin: '' }] })}><Plus size={14} />添加精确 origin</button>
            <small>仅 HTTP/HTTPS、字面量 IPv4/IPv6 和显式端口；不支持域名、路径、CIDR、通配符或端口范围。</small>
            {privateFetchValidationError ? <div className="form-error">{privateFetchValidationError}</div> : null}
          </div>}
        </section> : null}
      </> : <p className="muted-row">正在读取有效配置…</p>}
      {effectiveContext ? <>
        <SettingsSectionHeader title="上下文压缩" description="保存在数据库，对新启动的模型调用立即生效。" />
        <div className="settings-form-grid">
          <SettingRow label="自动压缩" meta="数据库 · 组织 · 立即生效"><input type="checkbox" checked={effectiveContext.auto_compact} onChange={(event) => setContextDraft({ ...contextDraft, auto_compact: event.target.checked })} /></SettingRow>
          <SettingRow label="触发 Token" meta="达到此值后开始压缩"><input type="number" min={1000} max={2000000} value={effectiveContext.trigger_tokens} onChange={(event) => setContextDraft({ ...contextDraft, trigger_tokens: Number(event.target.value) })} /></SettingRow>
          <SettingRow label="保留 Token" meta="压缩后保留最近上下文"><input type="number" min={1000} max={1000000} value={effectiveContext.retain_tokens} onChange={(event) => setContextDraft({ ...contextDraft, retain_tokens: Number(event.target.value) })} /></SettingRow>
          <SettingRow label="摘要上限" meta="单次摘要最大 Token"><input type="number" min={256} max={32768} value={effectiveContext.summary_max_tokens} onChange={(event) => setContextDraft({ ...contextDraft, summary_max_tokens: Number(event.target.value) })} /></SettingRow>
        </div>
      </> : <p className="muted-row">正在读取上下文策略…</p>}
      {saveAll.error ? <div className="form-error settings-inline-error">{displayError(saveAll.error)}</div> : null}
      <SettingsSaveBar changed={hasBootstrapChanges || hasContextChanges} pending={saveAll.isPending} restart={hasBootstrapChanges} disabled={privateFetchInvalid} label="保存本页更改" onSave={() => saveAll.mutate()} />
    </div>
  </SettingsLayout>
}

function GovernanceSettings() {
  const queryClient = useQueryClient()
  const settings = useQuery({ queryKey: ['ai-governance'], queryFn: api.aiGovernance })
  const providers = useQuery({ queryKey: ['providers'], queryFn: api.providers })
  const models = useQuery({ queryKey: ['models'], queryFn: api.models })
  const [draft, setDraft] = useState<Partial<AIGovernanceSettings>>({})
  const effective = settings.data ? { ...settings.data, ...draft } : undefined
  const providerModels = (models.data?.items ?? []).filter((item) => item.provider_id === effective?.reviewer_provider_id)
  const selectedProvider = providers.data?.items.find((item) => item.provider_id === effective?.reviewer_provider_id)
  const save = useMutation({
    mutationFn: () => api.updateAIGovernance({
      reviewer_provider_id: effective?.reviewer_provider_id ?? '',
      reviewer_model_id: effective?.reviewer_model_id ?? '',
      enabled: effective?.enabled ?? false,
      timeout_seconds: effective?.timeout_seconds ?? 30,
    }),
    onSuccess: async () => {
      setDraft({})
      await Promise.all([queryClient.invalidateQueries({ queryKey: ['ai-governance'] }), queryClient.invalidateQueries({ queryKey: ['diagnostics'] })])
    },
  })
  return <SettingsLayout active="governance">
    <div className="settings-group settings-ledger-page">
      <SettingsPageHeader icon={<ShieldCheck />} title="治理与审批" description="决定高风险动作由谁审查。Reviewer 是独立安全边界，不属于模型连接配置。" scope="组织策略" />
      <div className="governance-principle"><Bot size={22} /><span><strong>自动审查只处理策略已判定为 Ask 的 L2 动作</strong><small>Reviewer 没有工具、看不到密钥；不可用、超时或输出非法时一律转人工审批。</small></span><em>Fail-closed</em></div>
      {effective ? <div className="governance-form settings-form-grid">
        <SettingRow label="自动审查" meta="数据库 · 组织 · 新任务生效"><input aria-label="启用自动审查" type="checkbox" checked={effective.enabled} onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })} /></SettingRow>
        <SettingRow label="Reviewer Provider" meta={selectedProvider ? `${selectedProvider.display_name} · ${credentialStateText(selectedProvider)}` : '选择独立模型服务'}><select aria-label="Reviewer Provider" value={effective.reviewer_provider_id} onChange={(event) => setDraft({ ...draft, reviewer_provider_id: event.target.value, reviewer_model_id: '' })}><option value="">选择 Provider</option>{providers.data?.items.filter((item) => item.status === 'active').map((item) => <option key={item.provider_id} value={item.provider_id}>{item.display_name}</option>)}</select></SettingRow>
        <SettingRow label="Reviewer Model" meta="必须是所选 Provider 已启用的模型"><select aria-label="Reviewer Model" value={effective.reviewer_model_id} onChange={(event) => setDraft({ ...draft, reviewer_model_id: event.target.value })}><option value="">选择 Model</option>{providerModels.map((item) => <option key={item.id} value={item.id}>{item.id}</option>)}</select></SettingRow>
        <SettingRow label="响应超时" meta="超时后立即转人工"><select aria-label="Reviewer 超时" value={effective.timeout_seconds} onChange={(event) => setDraft({ ...draft, timeout_seconds: Number(event.target.value) })}><option value={15}>15 秒</option><option value={30}>30 秒</option><option value={60}>60 秒</option></select></SettingRow>
      </div> : <p className="muted-row">正在读取治理策略…</p>}
      {save.error ? <div className="form-error settings-inline-error">{displayError(save.error)}</div> : null}
      <SettingsSaveBar changed={Object.keys(draft).length > 0} pending={save.isPending} label="保存治理策略" onSave={() => save.mutate()} disabled={Boolean(effective?.enabled && (!effective.reviewer_provider_id || !effective.reviewer_model_id))} />
    </div>
  </SettingsLayout>
}

function PricingSettings() {
  const queryClient = useQueryClient()
  const pricing = useQuery({ queryKey: ['model-pricing'], queryFn: api.modelPricing })
  const providers = useQuery({ queryKey: ['providers'], queryFn: api.providers })
  const models = useQuery({ queryKey: ['models'], queryFn: api.models })
  const [pricingDraft, setPricingDraft] = useState({ provider_id: '', model_id: '', currency: 'USD', input_per_1k: '0', output_per_1k: '0', effective_at: localDateTime(new Date()), version: 1 })
  const [pricingDirty, setPricingDirty] = useState(false)
  const effectiveProviderID = pricingDraft.provider_id || providers.data?.items[0]?.provider_id || ''
  const providerModels = (models.data?.items ?? []).filter((item) => item.provider_id === effectiveProviderID)
  const effectiveModelID = pricingDraft.model_id || providerModels[0]?.id || ''
  const save = useMutation({
    mutationFn: () => api.updateModelPricing([{
      ...pricingDraft,
      provider_id: effectiveProviderID,
      model_id: effectiveModelID,
      effective_at: new Date(pricingDraft.effective_at).toISOString(),
    }]),
    onSuccess: async () => {
      setPricingDirty(false)
      await Promise.all([queryClient.invalidateQueries({ queryKey: ['model-pricing'] }), queryClient.invalidateQueries({ queryKey: ['diagnostics'] })])
    },
  })
  const valid = Boolean(effectiveProviderID && effectiveModelID && validDecimal(pricingDraft.input_per_1k) && validDecimal(pricingDraft.output_per_1k) && /^[A-Z]{3}$/.test(pricingDraft.currency))
  return <SettingsLayout active="pricing">
    <div className="settings-group settings-ledger-page">
      <SettingsPageHeader icon={<BadgeDollarSign />} title="费用与用量" description="为已启用模型登记版本化价格。每次调用按生效时间解析，并冻结到调用快照。" scope="组织计费" />
      <div className="pricing-editor settings-card-grid">
        <label><span>Provider</span><select aria-label="价格 Provider" value={effectiveProviderID} onChange={(event) => { setPricingDraft({ ...pricingDraft, provider_id: event.target.value, model_id: '' }); setPricingDirty(true) }}><option value="">选择 Provider</option>{providers.data?.items.map((item) => <option key={item.provider_id} value={item.provider_id}>{item.display_name}</option>)}</select></label>
        <label><span>Model</span><select aria-label="价格 Model" value={effectiveModelID} onChange={(event) => { setPricingDraft({ ...pricingDraft, model_id: event.target.value }); setPricingDirty(true) }}><option value="">选择 Model</option>{providerModels.map((item) => <option key={item.id} value={item.id}>{item.id}</option>)}</select></label>
        <label><span>输入 / 1K Token</span><input aria-label="输入价格" inputMode="decimal" value={pricingDraft.input_per_1k} onChange={(event) => { setPricingDraft({ ...pricingDraft, input_per_1k: event.target.value }); setPricingDirty(true) }} /></label>
        <label><span>输出 / 1K Token</span><input aria-label="输出价格" inputMode="decimal" value={pricingDraft.output_per_1k} onChange={(event) => { setPricingDraft({ ...pricingDraft, output_per_1k: event.target.value }); setPricingDirty(true) }} /></label>
        <label><span>币种</span><input aria-label="价格币种" maxLength={3} value={pricingDraft.currency} onChange={(event) => { setPricingDraft({ ...pricingDraft, currency: event.target.value.toUpperCase() }); setPricingDirty(true) }} /></label>
        <label><span>生效时间</span><input aria-label="价格生效时间" type="datetime-local" value={pricingDraft.effective_at} onChange={(event) => { setPricingDraft({ ...pricingDraft, effective_at: event.target.value }); setPricingDirty(true) }} /></label>
      </div>
      {save.error ? <div className="form-error settings-inline-error">{displayError(save.error)}</div> : null}
      <SettingsSaveBar changed={pricingDirty} pending={save.isPending} label="保存价格版本" onSave={() => save.mutate()} disabled={!valid} />
      <SettingsSectionHeader title="已生效与计划版本" description={`${pricing.data?.items.length ?? 0} 个价格版本；同一模型可按时间保留多版。`} />
      <PricingTable items={pricing.data?.items ?? []} providers={providers.data?.items ?? []} />
    </div>
  </SettingsLayout>
}

function VaultSettings() {
  const queryClient = useQueryClient()
  const vault = useQuery({ queryKey: ['vault'], queryFn: api.vaultStatus })
  const [recoveryCode, setRecoveryCode] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const initialize = useMutation({ mutationFn: api.initializeVault, onSuccess: (result) => { setRecoveryCode(result.recovery_code); void queryClient.invalidateQueries({ queryKey: ['vault'] }) } })
  const confirm = useMutation({ mutationFn: () => api.confirmVault(confirmation), onSuccess: async () => { setRecoveryCode(''); setConfirmation(''); await invalidateVault(queryClient) } })
  const recover = useMutation({ mutationFn: () => api.recoverVault(confirmation), onSuccess: async () => { setConfirmation(''); await invalidateVault(queryClient) } })
  const rotate = useMutation({ mutationFn: api.rotateVaultRecovery, onSuccess: (result) => { setRecoveryCode(result.recovery_code); void invalidateVault(queryClient) } })
  const reset = useMutation({ mutationFn: api.resetPendingVault, onSuccess: async () => { setRecoveryCode(''); setConfirmation(''); await invalidateVault(queryClient) } })
  const status = vault.data
  const error = initialize.error ?? confirm.error ?? recover.error ?? rotate.error ?? reset.error

  return <SettingsLayout active="vault">
    <div className="settings-group vault-settings">
      <header><h2>凭据与密钥库</h2><p>业务 Secret 以 AES-256-GCM 应用层密文进入数据库；本地 bootstrap.key 只负责自动解锁。</p></header>
      {status ? <div className={`vault-seal state-${status.state}`}>
        <div className="vault-seal-icon">{status.state === 'active' ? <ShieldCheck size={26} /> : <KeyRound size={26} />}</div>
        <div><span className="eyebrow">Database Vault</span><h3>{vaultStateLabel[status.state]}</h3><p>{status.detail}</p></div>
        <dl><div><dt>算法</dt><dd>{status.algorithm || '—'}</dd></div><div><dt>Key Version</dt><dd>{status.key_version || '—'}</dd></div><div><dt>Secret</dt><dd>{status.secret_count}</dd></div><div><dt>恢复包装</dt><dd>{status.recovery_configured ? '已配置' : '未配置'}</dd></div></dl>
      </div> : <p className="muted-row">正在读取密钥库状态…</p>}

      {status?.state === 'uninitialized' ? <VaultAction title="初始化数据库密钥库" description="系统将生成随机 DEK、本地 KEK 和一次性恢复口令。确认恢复口令前不能保存任何凭据。"><button className="primary-button" type="button" disabled={initialize.isPending} onClick={() => initialize.mutate()}>{initialize.isPending ? '正在生成…' : '生成恢复口令'}</button></VaultAction> : null}
      {status?.state === 'pending' ? <VaultAction title={recoveryCode ? '保存并确认恢复口令' : '初始化尚未确认'} description={recoveryCode ? '这是唯一一次显示。请复制或下载，然后在下方重新输入确认。页面不会持久化该口令。' : '恢复口令只在生成当次返回。若已丢失，请重置本次初始化并重新生成。'}>
        {recoveryCode ? <RecoveryCode code={recoveryCode} /> : null}
        {recoveryCode ? <label className="vault-secret-input"><span>重新输入恢复口令</span><input type="password" autoComplete="off" value={confirmation} onChange={(event) => setConfirmation(event.target.value)} /></label> : null}
        <div className="vault-action-buttons">{recoveryCode ? <button className="primary-button" type="button" disabled={confirm.isPending || confirmation !== recoveryCode} onClick={() => confirm.mutate()}>确认并启用 Vault</button> : null}<button className="secondary-button" type="button" disabled={reset.isPending} onClick={() => reset.mutate()}><RotateCcw size={14} />重置 Pending</button></div>
      </VaultAction> : null}
      {status?.state === 'locked' || status?.state === 'corrupt' ? <VaultAction title="使用恢复口令重新绑定本机" description={status.state === 'locked' ? '数据库来自另一台机器，当前缺少本地 KEK。解锁后系统会生成新的 bootstrap.key，后续重启自动解锁。' : '如果损坏仅发生在本地包装或 bootstrap.key，可用恢复口令重建本地包装；业务 Secret 密文损坏仍会保持 corrupt。'}><label className="vault-secret-input"><span>恢复口令</span><input type="password" autoComplete="off" value={confirmation} onChange={(event) => setConfirmation(event.target.value)} /></label><button className="primary-button" type="button" disabled={recover.isPending || !confirmation.trim()} onClick={() => recover.mutate()}>恢复并重新绑定本机</button></VaultAction> : null}
      {status?.state === 'active' ? <VaultAction title="轮换恢复口令" description="不会轮换业务 DEK，也不会改写每条 Secret；仅重新生成恢复包装。旧恢复口令立即失效。">{recoveryCode ? <RecoveryCode code={recoveryCode} /> : <button className="secondary-button" type="button" disabled={rotate.isPending} onClick={() => rotate.mutate()}><RotateCcw size={14} />生成新的恢复口令</button>}</VaultAction> : null}
      {status?.state === 'corrupt' ? <div className="settings-danger"><AlertTriangle size={18} /><span><strong>密钥库元数据、本地包装或密文已损坏</strong><small>系统不会用空值回退。若恢复口令无法修复，请从正式备份恢复，或保留项目数据后重建密钥库。</small></span></div> : null}
      {error ? <div className="form-error settings-inline-error">{error.message}</div> : null}
      <div className="credential-destinations">
        <Link to="/settings/providers"><Server size={18} /><span><strong>Provider 凭据</strong><small>在模型服务中录入 API Key，与连接和模型一次保存。</small></span></Link>
        <Link to="/resources"><KeyRound size={18} /><span><strong>MCP 凭据与 OAuth</strong><small>在资源中心选择 MCP，可随时录入、更新凭据或重新连接账号。</small></span></Link>
        <Link to="/diagnostics"><CheckCircle2 size={18} /><span><strong>检查缺失项</strong><small>诊断会列出具体 Provider/MCP 名称和修复入口。</small></span></Link>
      </div>
    </div>
  </SettingsLayout>
}

function DataSettings() {
  const configuration = useQuery({ queryKey: ['configuration'], queryFn: api.configuration })
  const vault = useQuery({ queryKey: ['vault'], queryFn: api.vaultStatus })
  return <SettingsLayout active="data">
    <div className="settings-group settings-ledger-page">
      <SettingsPageHeader icon={<HardDrive />} title="数据与备份" description="系统备份和项目交换是两条不同边界：前者恢复整个 Oktapod，后者只移动一个项目的脱敏内容。" scope="系统 + 项目" />
      <SettingsSectionHeader title="系统级备份与恢复" description="覆盖数据库、全部项目、Workspace、Blob、Vault 密文和恢复包装；属于高风险运维操作。" />
      <div className="portability-grid">
        <article><Database size={20} /><h3>SQLite 数据库</h3><p>包含项目数据、Secret 密文及恢复包装，不包含明文 Secret。</p><code>{configuration.data?.data_root || '默认 Data Root'}</code></article>
        <article><HardDrive size={20} /><h3>正式 Backup V2</h3><p>包含数据库密文与 Vault Manifest；明确排除 bootstrap.key、恢复口令和运行缓存。</p><span>{vault.data?.secret_count ?? 0} 条 Secret 密文</span></article>
        <article><KeyRound size={20} /><h3>本机自动解锁</h3><p>bootstrap.key 位于 Data Root 的 vault 目录。复制完整 Data Root 会把它与数据库一起复制。</p><span>仅当前服务用户可读写</span></article>
      </div>
      <div className="system-backup-boundary"><ShieldCheck size={18} /><span><strong>系统备份当前只允许通过本机 CLI 执行</strong><small><code>oktapod backup</code> / <code>oktapod restore</code>。新机器恢复后仍需恢复口令才能解锁 Secret。</small></span><em>系统级</em></div>
      <SettingsSectionHeader title="项目交换" description="仅导出所选项目的会话和可移植内容；不含 Provider、MCP、登录会话、系统设置或任何 Secret。" />
      <div className="project-portability-action"><div><strong>导出或导入一个个人项目</strong><p>适合项目交接和内容迁移，不可用于整机灾难恢复。</p></div><Link className="primary-button" to="/import-export">打开项目交换</Link></div>
      <div className="settings-link-row"><Link className="secondary-button" to="/diagnostics">查看配置诊断</Link></div>
    </div>
  </SettingsLayout>
}

function AppearanceSettings() {
  const queryClient = useQueryClient()
  const remote = useQuery({ queryKey: ['preferences'], queryFn: api.preferences })
  const localDefaults = useState(() => {
    const local = safeLocalPreferences()
    return { ...local, default_permission: 'ask', recent_provider_id: '', recent_model_id: '', updated_at: '' } as UserPreferences
  })[0]
  const [preferenceDraft, setPreferenceDraft] = useState<Partial<UserPreferences>>({})
  const preferences = { ...localDefaults, ...remote.data, ...preferenceDraft }
  const presentation = useMemo(() => ({ theme: preferences.theme, density: preferences.density }), [preferences.density, preferences.theme])
  useEffect(() => {
    window.localStorage.setItem(presentationPreferenceStorageKey, JSON.stringify(presentation))
    applyPresentationPreferences(presentation)
  }, [presentation])
  const save = useMutation({
    mutationFn: () => api.updatePreferences({ theme: preferences.theme, density: preferences.density, default_permission: preferences.default_permission, recent_provider_id: preferences.recent_provider_id, recent_model_id: preferences.recent_model_id }),
    onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['preferences'] }),
  })
  return <SettingsLayout active="appearance">
    <div className="settings-group"><header><h2>外观与个人偏好</h2><p>主题和密度立即应用于当前设备，并同步为账户偏好；草稿、侧栏和 Inspector 页签仍只保存在当前设备。</p></header>
      <SettingRow label="主题" meta="来源：数据库 + 当前设备 · 作用域：用户 · 立即生效"><select aria-label="主题" value={preferences.theme} onChange={(event) => setPreferenceDraft({ ...preferenceDraft, theme: event.target.value as UserPreferences['theme'] })}><option value="system">跟随系统</option><option value="light">浅色</option><option value="dark">深色</option></select></SettingRow>
      <SettingRow label="信息密度" meta="来源：数据库 + 当前设备 · 作用域：用户 · 立即生效"><select aria-label="信息密度" value={preferences.density} onChange={(event) => setPreferenceDraft({ ...preferenceDraft, density: event.target.value as UserPreferences['density'] })}><option value="compact">紧凑</option><option value="comfortable">舒适</option></select></SettingRow>
      <SettingRow label="默认权限" meta="来源：数据库 · 作用域：用户 · 新会话生效"><select aria-label="默认权限" value={preferences.default_permission} onChange={(event) => setPreferenceDraft({ ...preferenceDraft, default_permission: event.target.value as UserPreferences['default_permission'] })}><option value="ask">请我审批</option><option value="auto_review">自动审查</option></select></SettingRow>
      <p className="settings-warning">“完全访问”不会保存为跨设备默认值；它仍需在当前设备和具体项目中显式选择。</p>
      {save.error ? <div className="form-error settings-inline-error">{save.error.message}</div> : null}
      <button className="secondary-button" type="button" disabled={save.isPending} onClick={() => save.mutate()}><Save size={14} />保存个人偏好</button>
    </div>
  </SettingsLayout>
}

export function SettingsLayout({ active, children }: { active: SettingsNavigation; children: React.ReactNode }) {
  return <main className="settings-page">
    <header className="management-heading settings-heading"><div><span className="eyebrow">Oktapod control plane</span><h1>配置中心</h1><p>按职责管理连接、治理、运行、费用和数据边界；密钥永不回填到页面。</p></div></header>
    <div className="settings-layout">
      <nav aria-label="设置分组">
        <span className="settings-nav-label">能力</span>
        <SettingsNavLink active={active === 'providers'} to="/settings/providers" icon={<Server size={16} />} label="模型服务" meta="连接与模型" />
        <SettingsNavLink active={active === 'governance'} to="/settings/governance" icon={<ShieldCheck size={16} />} label="治理与审批" meta="Reviewer 策略" />
        <span className="settings-nav-label">运行</span>
        <SettingsNavLink active={active === 'runtime'} to="/settings/runtime" icon={<SlidersHorizontal size={16} />} label="运行与上下文" meta="启动与压缩" />
        <SettingsNavLink active={active === 'pricing'} to="/settings/pricing" icon={<BadgeDollarSign size={16} />} label="费用与用量" meta="模型价格" />
        <span className="settings-nav-label">安全与数据</span>
        <SettingsNavLink active={active === 'vault'} to="/settings/vault" icon={<KeyRound size={16} />} label="凭据与密钥库" meta="加密与恢复" />
        <SettingsNavLink active={active === 'data'} to="/settings/data" icon={<HardDrive size={16} />} label="数据与备份" meta="系统 / 项目边界" />
        <SettingsNavLink active={active === 'appearance'} to="/settings/appearance" icon={<Palette size={16} />} label="外观与偏好" meta="账户与设备" />
      </nav>
      <section className="settings-detail">{children}</section>
    </div>
  </main>
}

function SettingsNavLink({ active, to, icon, label, meta }: { active: boolean; to: string; icon: React.ReactNode; label: string; meta: string }) {
  return <Link className={active ? 'active' : ''} to={to}><span className="settings-nav-icon">{icon}</span><span><strong>{label}</strong><small>{meta}</small></span></Link>
}

function SettingsPageHeader({ icon, title, description, scope }: { icon: React.ReactNode; title: string; description: string; scope: string }) {
  return <header className="settings-page-header"><span className="settings-page-icon">{icon}</span><div><h2>{title}</h2><p>{description}</p></div><em>{scope}</em></header>
}

function SettingsSectionHeader({ title, description }: { title: string; description: string }) {
  return <div className="settings-section-header"><strong>{title}</strong><span>{description}</span></div>
}

function SettingsSaveBar({ changed, pending, restart, disabled, label, onSave }: { changed: boolean; pending: boolean; restart?: boolean; disabled?: boolean; label: string; onSave: () => void }) {
  return <div className="settings-save-bar"><span><i className={changed ? 'changed' : ''} /><strong>{changed ? '有未保存的更改' : '当前已保存'}</strong><small>{restart ? '其中包含重启后生效的服务配置' : '保存后按页面标注的作用域生效'}</small></span><button className="primary-button" type="button" disabled={disabled || pending || !changed} onClick={onSave}><Save size={14} />{pending ? '保存中…' : label}</button></div>
}

function SettingRow({ label, meta, children }: { label: string; meta: string; children: React.ReactNode }) {
  return <label className="setting-row"><span><strong>{label}</strong><small>{meta}</small></span><span>{children}</span></label>
}

function ConfigSourceCard({ configuration, saved }: { configuration: BootstrapConfiguration; saved: boolean }) {
  return <div className={`config-source-card ${saved ? 'restart' : ''}`}>{saved ? <AlertTriangle size={17} /> : <CheckCircle2 size={17} />}<span><strong>{saved ? '配置已保存，等待重启生效' : '当前有效启动配置'}</strong><small>{configuration.config_path} · 来源：{configuration.source}</small></span></div>
}

function PricingTable({ items, providers }: { items: ModelPricingVersion[]; providers: ProviderInstance[] }) {
  if (!items.length) return <p className="muted-row">尚未配置价格版本；未命中的调用费用保持未知，不使用环境变量回退。</p>
  const providerNames = new Map(providers.map((provider) => [provider.provider_id, provider.display_name]))
  return <div className="pricing-table" role="table"><div role="row"><strong>Provider / Model</strong><strong>价格 / 1K</strong><strong>生效时间</strong></div>{items.map((item) => <div role="row" key={`${item.provider_id}:${item.model_id}:${item.effective_at}:${item.version}`}><span>{providerNames.get(item.provider_id) ?? '已删除的 Provider'}<small>{item.model_id} · v{item.version}</small></span><span>{item.currency} {item.input_per_1k} / {item.output_per_1k}</span><time>{formatDate(item.effective_at)}</time></div>)}</div>
}

function VaultAction({ title, description, children }: { title: string; description: string; children: React.ReactNode }) {
  return <section className="vault-action"><header><h3>{title}</h3><p>{description}</p></header>{children}</section>
}

function RecoveryCode({ code }: { code: string }) {
  const copy = () => void navigator.clipboard.writeText(code)
  const download = () => {
    const blob = new Blob([`${code}\n`], { type: 'text/plain;charset=utf-8' })
    const href = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = href
    anchor.download = 'oktapod-recovery-code.txt'
    anchor.click()
    URL.revokeObjectURL(href)
  }
  return <div className="recovery-code"><code>{code}</code><button type="button" aria-label="复制恢复口令" title="复制" onClick={copy}><Copy size={14} /></button><button type="button" aria-label="下载恢复口令" title="下载" onClick={download}><Download size={14} /></button></div>
}

function sourceMeta(configuration: BootstrapConfiguration, restart: boolean) {
  return `来源：${configuration.source} · 作用域：服务 · ${restart ? '重启生效' : '立即生效'}`
}

function safeLocalPreferences() {
  try { return { ...defaultPresentationPreferences, ...readPresentationPreferences() } } catch { return defaultPresentationPreferences }
}

function localDateTime(date: Date) {
  const offset = date.getTimezoneOffset() * 60_000
  return new Date(date.getTime() - offset).toISOString().slice(0, 16)
}

function formatDate(value: string) {
  try { return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) } catch { return value }
}

function isSettingsSection(value: string): value is SettingsSection {
  return value === 'governance' || value === 'runtime' || value === 'pricing' || value === 'vault' || value === 'data' || value === 'appearance'
}

function credentialStateText(provider?: ProviderInstance) {
  if (!provider) return '未选择'
  return ({ ready: '凭据可用', locked: '密钥库锁定', missing: '凭据缺失', corrupt: '密文损坏' } as const)[provider.credential_state]
}

function validDecimal(value: string) {
  return /^[0-9]+(?:\.[0-9]+)?$/.test(value)
}

async function invalidateVault(queryClient: ReturnType<typeof useQueryClient>) {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: ['vault'] }),
    queryClient.invalidateQueries({ queryKey: ['providers'] }),
    queryClient.invalidateQueries({ queryKey: ['diagnostics'] }),
  ])
}

const vaultStateLabel = { uninitialized: '尚未初始化', pending: '等待确认', active: '已解锁', locked: '已锁定', corrupt: '数据损坏' } as const
