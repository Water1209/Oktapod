import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, Check, CheckCircle2, KeyRound, Plus, RefreshCw, Search, Server, Trash2 } from 'lucide-react'
import { api, displayError, type ProviderInstance, type ProviderModelOption, type ProviderTestResult } from '../../shared/api/client'
import { SettingsLayout } from '../settings/settings-page'

type ProviderDraft = {
  display_name: string
  protocol_family: ProviderInstance['protocol_family']
  endpoint: string
  credential: string
  status: ProviderInstance['status']
  is_default: boolean
}
type ProviderSaveResult = { provider: ProviderInstance; test?: ProviderTestResult; testError?: string }

const emptyDraft: ProviderDraft = { display_name: '', protocol_family: 'openai_compatible', endpoint: 'https://api.openai.com/v1', credential: '', status: 'disabled', is_default: false }

export function ProviderSettingsPage() {
  const queryClient = useQueryClient()
  const providers = useQuery({ queryKey: ['providers'], queryFn: api.providers })
  const vault = useQuery({ queryKey: ['vault'], queryFn: api.vaultStatus })
  const [selectedId, setSelectedId] = useState<string>()
  const [creating, setCreating] = useState(false)
  const [savedFeedback, setSavedFeedback] = useState<ProviderSaveResult>()
  const selected = providers.data?.items.find((item) => item.provider_id === selectedId)
  const readyCount = providers.data?.items.filter((item) => item.credential_state === 'ready').length ?? 0

  useEffect(() => {
    if (!creating && !selectedId && providers.data?.items[0]) setSelectedId(providers.data.items[0].provider_id)
  }, [creating, providers.data, selectedId])

  return <SettingsLayout active="providers">
    <div className="settings-group provider-settings-surface">
      <header className="provider-settings-heading"><span className="settings-page-icon"><Server size={19} /></span><div><h2>模型服务</h2><p>一个实例同时管理连接、密钥和可用模型；所有修改在底部一次保存。</p></div><div className="provider-heading-stats"><span><strong>{providers.data?.items.length ?? 0}</strong><small>实例</small></span><span><strong>{readyCount}</strong><small>凭据可用</small></span></div><button className="primary-button compact" type="button" onClick={() => { setCreating(true); setSelectedId(undefined); setSavedFeedback(undefined) }}><Plus size={15} />添加实例</button></header>
      {vault.data?.state !== 'active' ? <div className="settings-attention"><AlertTriangle size={17} /><span><strong>数据库密钥库尚未可用</strong><small>{vault.data?.detail || '先初始化密钥库，再录入 Provider 凭据。'}</small></span><a href="/settings/vault">打开凭据与密钥库</a></div> : null}
      <div className="master-detail provider-master-detail">
        <aside className="master-list provider-instance-list" aria-label="模型服务实例">
          <div className="provider-list-label">连接实例</div>
          {providers.isLoading ? <p className="muted-row">正在读取实例…</p> : null}
          {providers.data?.items.map((provider) => <button key={provider.provider_id} type="button" className={provider.provider_id === selectedId ? 'active' : ''} onClick={() => { setCreating(false); setSelectedId(provider.provider_id); setSavedFeedback(undefined) }}>
            <span className={`status-orb ${provider.credential_state === 'ready' && provider.status === 'active' ? 'active' : provider.status}`} /><span><strong>{provider.display_name}</strong><small>{provider.default_model_id || '尚未启用模型'}</small><small className={`credential-copy ${provider.credential_state}`}>{credentialStateLabel[provider.credential_state]}</small></span>{provider.is_default ? <em>默认</em> : null}
          </button>)}
          {!providers.isLoading && providers.data?.items.length === 0 ? <div className="master-empty"><Server size={20} /><strong>尚未配置</strong><span>添加一个连接，并指定至少一个可用模型。</span></div> : null}
        </aside>
        <section className="detail-panel">
          {creating ? <ProviderEditor key="new" onCancel={() => setCreating(false)} onSaved={(item, result) => { setSavedFeedback(result); setCreating(false); setSelectedId(item.provider_id) }} /> : selected ? <ProviderEditor key={selected.provider_id} provider={selected} initialSaveResult={savedFeedback?.provider.provider_id === selected.provider_id ? savedFeedback : undefined} onSaved={(item, result) => { setSavedFeedback(result); setSelectedId(item.provider_id) }} onDeleted={() => { setSavedFeedback(undefined); setSelectedId(undefined) }} /> : <div className="detail-empty"><Server size={28} /><h2>选择一个模型服务</h2><p>凭据只显示状态，旧值永不回填到浏览器。</p></div>}
        </section>
      </div>
    </div>
  </SettingsLayout>

  function ProviderEditor({ provider, initialSaveResult, onCancel, onSaved, onDeleted }: { provider?: ProviderInstance; initialSaveResult?: ProviderSaveResult; onCancel?: () => void; onSaved: (item: ProviderInstance, result: ProviderSaveResult) => void; onDeleted?: () => void }) {
    const [draft, setDraft] = useState<ProviderDraft>(provider ? { display_name: provider.display_name, protocol_family: provider.protocol_family, endpoint: provider.endpoint, credential: '', status: provider.status, is_default: provider.is_default } : emptyDraft)
    const models = useQuery({ queryKey: ['provider-models', provider?.provider_id], queryFn: () => api.providerModels(provider!.provider_id), enabled: Boolean(provider), retry: false })
    const [selectedModels, setSelectedModels] = useState<string[]>([])
    const [defaultModel, setDefaultModel] = useState(provider?.default_model_id ?? '')
    const [search, setSearch] = useState('')
    const [manual, setManual] = useState('')
    const [saveResult, setSaveResult] = useState<ProviderSaveResult | undefined>(initialSaveResult)

    useEffect(() => {
      if (!models.data) return
      setSelectedModels(models.data.items.filter((item) => item.enabled).map((item) => item.model_id))
      setDefaultModel(models.data.items.find((item) => item.default)?.model_id || provider?.default_model_id || '')
    }, [models.data, provider?.default_model_id])

    const originalModels = useMemo(() => (models.data?.items ?? []).filter((item) => item.enabled).map((item) => item.model_id).sort(), [models.data])
    const dirty = !provider || Boolean(draft.credential) || draft.display_name !== provider.display_name || draft.protocol_family !== provider.protocol_family || draft.endpoint !== provider.endpoint || draft.status !== provider.status || draft.is_default !== provider.is_default || defaultModel !== provider.default_model_id || selectedModels.slice().sort().join('\0') !== originalModels.join('\0')
    const options = useMemo(() => mergeManualOptions(models.data?.items ?? [], selectedModels).filter((item) => item.model_id.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase())), [models.data, search, selectedModels])

    const saveAll = useMutation({
      mutationFn: async () => {
        const saved = await api.saveProvider(provider?.provider_id, {
          display_name: draft.display_name.trim(),
          protocol_family: draft.protocol_family,
          endpoint: draft.endpoint.trim(),
          default_model_id: defaultModel || provider?.default_model_id || undefined,
          credential: draft.credential || undefined,
          status: draft.status,
          is_default: draft.is_default,
        })
        if (selectedModels.length > 0 && defaultModel) {
          await api.saveProviderModels(saved.provider_id, { model_ids: selectedModels, default_model_id: defaultModel, activate: draft.status === 'active' })
        }
        try {
          const test = await api.testProvider(saved.provider_id)
          return { provider: saved, test }
        } catch (error) {
          return { provider: saved, testError: displayError(error) }
        }
      },
      onSuccess: async (result) => {
        setDraft((current) => ({ ...current, credential: '' }))
        setSaveResult(result)
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: ['providers'] }),
          queryClient.invalidateQueries({ queryKey: ['provider-models', result.provider.provider_id] }),
          queryClient.invalidateQueries({ queryKey: ['models'] }),
          queryClient.invalidateQueries({ queryKey: ['diagnostics'] }),
        ])
        onSaved(result.provider, result)
      },
    })
    const remove = useMutation({ mutationFn: () => api.deleteProvider(provider!.provider_id), onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['providers'] }),
        queryClient.invalidateQueries({ queryKey: ['models'] }),
        queryClient.invalidateQueries({ queryKey: ['projects'] }),
        queryClient.invalidateQueries({ queryKey: ['preferences'] }),
        queryClient.invalidateQueries({ queryKey: ['ai-governance'] }),
        queryClient.invalidateQueries({ queryKey: ['diagnostics'] }),
      ])
      onDeleted?.()
    } })
    const testConnection = useMutation({ mutationFn: () => api.testProvider(provider!.provider_id), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['provider-models', provider!.provider_id] }) })
    const refresh = useMutation({ mutationFn: () => api.refreshProviderModels(provider!.provider_id), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['provider-models', provider!.provider_id] }) })
    const latestTest = testConnection.data ?? saveResult?.test
    const latestTestError = testConnection.error ? displayError(testConnection.error) : saveResult?.testError
    const testSucceeded = latestTest?.stage === 'complete' && !latestTest.redacted_error
    const toggle = (modelID: string) => setSelectedModels((items) => items.includes(modelID) ? items.filter((item) => item !== modelID) : [...items, modelID])
    const addManual = () => { const modelID = manual.trim(); if (!modelID || modelID.includes('\n') || modelID.includes('\r')) return; setSelectedModels((items) => items.includes(modelID) ? items : [...items, modelID]); setDefaultModel((value) => value || modelID); setManual('') }
    const canSave = vault.data?.state === 'active' && Boolean(draft.display_name.trim() && draft.endpoint.trim()) && Boolean(provider || draft.credential) && (selectedModels.length === 0 || selectedModels.includes(defaultModel))

    return <form className="provider-editor provider-combined-form" onSubmit={(event) => { event.preventDefault(); saveAll.mutate() }}>
      <section className="provider-editor-section connection-section">
        <header><div><span className="eyebrow">01 / 连接</span><h2>{provider?.display_name || '添加模型服务'}</h2><p>连接元数据与密钥一起提交；密钥写入数据库 Vault 后立即从表单清空。</p></div>{provider ? <span className={`connection-badge ${provider.status}`}>{provider.status === 'active' ? '已启用' : provider.status === 'error' ? '异常' : '已停用'}</span> : null}</header>
        <div className="form-grid provider-connection-grid">
          <label className="form-field"><span>显示名称</span><input required value={draft.display_name} onChange={(event) => setDraft({ ...draft, display_name: event.target.value })} placeholder="例如：火山方舟" /></label>
          <label className="form-field"><span>协议族</span><select value={draft.protocol_family} onChange={(event) => { const protocol = event.target.value as ProviderDraft['protocol_family']; const endpoint = protocol === 'anthropic' ? 'https://api.anthropic.com/v1' : protocol === 'gemini' ? 'https://generativelanguage.googleapis.com/v1beta' : 'https://api.openai.com/v1'; setDraft({ ...draft, protocol_family: protocol, ...(!provider ? { endpoint } : {}) }) }}><option value="openai_compatible">OpenAI compatible</option><option value="anthropic">Anthropic native</option><option value="gemini">Gemini native</option></select></label>
          <label className="form-field wide"><span>Endpoint</span><input required type="url" value={draft.endpoint} onChange={(event) => setDraft({ ...draft, endpoint: event.target.value })} /></label>
          <label className="form-field"><span>状态</span><select value={draft.status} onChange={(event) => setDraft({ ...draft, status: event.target.value as ProviderDraft['status'] })}><option value="active">启用</option><option value="disabled">停用</option></select></label>
          <label className="form-field wide"><span><KeyRound size={13} />API Key <em>{provider?.credential_configured ? '已配置 · 留空保持不变' : provider ? credentialStateLabel[provider.credential_state] : '首次保存必填'}</em></span><input required={!provider} disabled={vault.data?.state !== 'active'} type="password" autoComplete="new-password" value={draft.credential} onChange={(event) => setDraft({ ...draft, credential: event.target.value })} placeholder={vault.data?.state !== 'active' ? '先初始化或恢复数据库密钥库' : provider?.credential_configured ? '••••••••（不会读取旧值）' : '输入新凭据'} /></label>
        </div>
        <label className="check-row provider-default-row"><input type="checkbox" checked={draft.is_default} onChange={(event) => setDraft({ ...draft, is_default: event.target.checked })} /><span><strong>设为默认实例</strong><small>新项目优先使用此模型服务。</small></span></label>
      </section>

      <section className="provider-editor-section provider-models-section">
        <header><div><span className="eyebrow">02 / 模型</span><h3>会出现在项目和会话中的模型</h3><p>目录读取不自动启用模型；手动模型的工具能力保持“未知”。</p></div><button className="secondary-button compact" type="button" disabled={!provider || refresh.isPending || dirty} title={dirty ? '先保存当前连接更改' : undefined} onClick={() => refresh.mutate()}><RefreshCw className={refresh.isPending ? 'spin' : ''} size={14} />从 API 读取</button></header>
        <div className="provider-model-toolbar"><label><Search size={14} /><input aria-label="搜索模型" placeholder="搜索模型" value={search} onChange={(event) => setSearch(event.target.value)} /></label><div><input aria-label="手动模型 ID" placeholder="手动添加模型 ID" maxLength={255} value={manual} onChange={(event) => setManual(event.target.value)} /><button type="button" disabled={!manual.trim()} onClick={addManual}><Plus size={14} />添加</button></div></div>
        <div className="provider-model-list">{options.map((item) => <label key={item.model_id} className={selectedModels.includes(item.model_id) ? 'selected' : ''}><input type="checkbox" checked={selectedModels.includes(item.model_id)} onChange={() => toggle(item.model_id)} /><span><strong>{item.model_id}</strong><small>{item.source === 'manual' ? '手动添加 · 能力未知' : `${item.stale ? '缓存已过期' : 'API 目录'}${item.context_window ? ` · ${item.context_window.toLocaleString()} context` : ''}`}</small></span>{defaultModel === item.model_id ? <em>默认</em> : selectedModels.includes(item.model_id) ? <Check size={14} /> : null}</label>)}</div>
        <label className="form-field model-default-select"><span>默认模型</span><select aria-label="默认模型" value={defaultModel} onChange={(event) => setDefaultModel(event.target.value)}><option value="">选择默认模型</option>{selectedModels.map((modelID) => <option key={modelID} value={modelID}>{modelID}</option>)}</select></label>
      </section>

      {latestTest || latestTestError ? <div className={`test-result provider-test-result ${testSucceeded ? '' : 'error'}`} role="status">{testSucceeded ? <CheckCircle2 size={18} /> : <AlertTriangle size={18} />}<span><strong>{testSucceeded ? '保存并验证成功' : saveResult?.provider ? '更改已保存，连接验证未通过' : '连接验证未通过'}</strong><small>{testSucceeded ? `${latestTest?.model_count ?? 0} 个模型 · ${latestTest?.latency_millis ?? 0} ms` : latestTest?.redacted_error || latestTestError || `失败阶段：${latestTest?.stage ?? 'unknown'}`}</small></span></div> : null}
      {saveAll.error || remove.error || refresh.error ? <div className="form-error provider-form-error">{displayError(saveAll.error ?? remove.error ?? refresh.error)}</div> : null}
      <footer className="provider-save-bar">
        <div className="provider-destructive-actions">{provider ? <button className="danger-button" type="button" disabled={remove.isPending} onClick={() => { if (window.confirm(`删除 ${provider.display_name}？项目、Agent 和最近模型选择会切换到剩余默认实例；历史 Run 不会改写。`)) remove.mutate() }}><Trash2 size={14} />{remove.isPending ? '删除中…' : '删除实例'}</button> : <button className="secondary-button" type="button" onClick={onCancel}>取消</button>}{provider ? <button className="secondary-button" type="button" disabled={testConnection.isPending || provider.credential_state !== 'ready' || dirty} onClick={() => testConnection.mutate()}><RefreshCw className={testConnection.isPending ? 'spin' : ''} size={14} />{testConnection.isPending ? '测试中…' : '测试已保存连接'}</button> : null}</div>
        <span><i className={dirty ? 'changed' : ''} /><strong>{dirty ? '有未保存的更改' : '当前已保存'}</strong><small>{selectedModels.length} 个模型 · {defaultModel || '未选择默认模型'}</small></span>
        <button className="primary-button" type="submit" disabled={!dirty || saveAll.isPending || !canSave}>{saveAll.isPending ? '保存并验证中…' : '保存所有更改'}</button>
      </footer>
    </form>
  }
}

const credentialStateLabel = { ready: '凭据可用', locked: '密钥库锁定', missing: '凭据缺失', corrupt: '密文损坏' } as const

function mergeManualOptions(catalog: ProviderModelOption[], selected: string[]) {
  const byID = new Map(catalog.map((item) => [item.model_id, item]))
  for (const modelID of selected) if (!byID.has(modelID)) byID.set(modelID, { provider_id: '', model_id: modelID, source: 'manual', enabled: true, default: false, capabilities: {}, context_window: 0, max_output: 0, fetched_at: '', expires_at: '', catalog_version: '', stale: false })
  return [...byID.values()]
}
