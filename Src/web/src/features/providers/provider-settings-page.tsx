import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, Check, CheckCircle2, KeyRound, Plus, RefreshCw, Search, Server, Trash2 } from 'lucide-react'
import { api, type ProviderInstance, type ProviderModelOption } from '../../shared/api/client'
import { SettingsLayout } from '../settings/settings-page'

type ProviderDraft = {
  display_name: string
  protocol_family: ProviderInstance['protocol_family']
  endpoint: string
  credential: string
  status: ProviderInstance['status']
  is_default: boolean
}

const emptyDraft: ProviderDraft = { display_name: '', protocol_family: 'openai_compatible', endpoint: 'https://api.openai.com/v1', credential: '', status: 'disabled', is_default: false }

export function ProviderSettingsPage() {
  const queryClient = useQueryClient()
  const providers = useQuery({ queryKey: ['providers'], queryFn: api.providers })
  const [selectedId, setSelectedId] = useState<string>()
  const [creating, setCreating] = useState(false)
  const selected = providers.data?.items.find((item) => item.provider_id === selectedId)

  useEffect(() => {
    if (!creating && !selectedId && providers.data?.items[0]) setSelectedId(providers.data.items[0].provider_id)
  }, [creating, providers.data, selectedId])

  return <SettingsLayout active="providers">
    <div className="provider-settings-heading"><div><h2>模型服务</h2><p>连接配置负责“怎么访问”，可用模型负责“允许选择什么”。</p></div><button className="primary-button compact" type="button" onClick={() => { setCreating(true); setSelectedId(undefined) }}><Plus size={15} />添加实例</button></div>
    <div className="master-detail provider-master-detail">
      <aside className="master-list" aria-label="模型服务实例">
        {providers.isLoading ? <p className="muted-row">正在读取实例…</p> : null}
        {providers.data?.items.map((provider) => <button key={provider.provider_id} type="button" className={provider.provider_id === selectedId ? 'active' : ''} onClick={() => { setCreating(false); setSelectedId(provider.provider_id) }}>
          <span className={`status-orb ${provider.status}`} /><span><strong>{provider.display_name}</strong><small>{provider.default_model_id || '尚未启用模型'}</small></span>{provider.is_default ? <em>默认</em> : null}
        </button>)}
        {!providers.isLoading && providers.data?.items.length === 0 ? <div className="master-empty"><Server size={20} /><strong>尚未配置</strong><span>先添加连接，再从 API 读取或手动添加模型。</span></div> : null}
      </aside>
      <section className="detail-panel">
        {creating ? <ProviderEditor key="new" onCancel={() => setCreating(false)} onSaved={(item) => { setCreating(false); setSelectedId(item.provider_id) }} /> : selected ? <ProviderEditor key={selected.provider_id} provider={selected} onSaved={(item) => setSelectedId(item.provider_id)} onDeleted={() => setSelectedId(undefined)} /> : <div className="detail-empty"><Server size={28} /><h2>选择一个模型服务</h2><p>凭据只显示配置状态，旧值永不回填到浏览器。</p></div>}
      </section>
    </div>
  </SettingsLayout>

  function ProviderEditor({ provider, onCancel, onSaved, onDeleted }: { provider?: ProviderInstance; onCancel?: () => void; onSaved: (item: ProviderInstance) => void; onDeleted?: () => void }) {
    const [draft, setDraft] = useState<ProviderDraft>(provider ? { display_name: provider.display_name, protocol_family: provider.protocol_family, endpoint: provider.endpoint, credential: '', status: provider.status, is_default: provider.is_default } : emptyDraft)
    const save = useMutation({
      mutationFn: () => api.saveProvider(provider?.provider_id, { display_name: draft.display_name.trim(), protocol_family: draft.protocol_family, endpoint: draft.endpoint.trim(), default_model_id: provider?.default_model_id || undefined, credential: draft.credential || undefined, status: draft.status, is_default: draft.is_default }),
      onSuccess: async (item) => {
        await queryClient.invalidateQueries({ queryKey: ['providers'] })
        onSaved(item)
        if (!provider) {
          try { await api.refreshProviderModels(item.provider_id) } catch { /* The model panel shows an explicit retry action. */ }
          await queryClient.invalidateQueries({ queryKey: ['provider-models', item.provider_id] })
        }
      },
    })
    const remove = useMutation({ mutationFn: () => api.deleteProvider(provider!.provider_id), onSuccess: async () => { await queryClient.invalidateQueries({ queryKey: ['providers'] }); onDeleted?.() } })
    const testConnection = useMutation({
      mutationFn: () => api.testProvider(provider!.provider_id),
      onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['provider-models', provider!.provider_id] }),
    })
    const testSucceeded = testConnection.data?.stage === 'complete' && !testConnection.data.redacted_error
    return <div className="provider-editor">
      <form className="provider-form connection-form" onSubmit={(event) => { event.preventDefault(); save.mutate() }}>
        <header><div><span className="eyebrow">连接配置</span><h2>{provider?.display_name || '添加模型服务'}</h2><p>保存新连接后保持停用，选择模型时再启用。</p></div>{provider ? <span className={`connection-badge ${provider.status}`}>{provider.status === 'active' ? '已启用' : provider.status === 'error' ? '异常' : '已停用'}</span> : null}</header>
        <div className="form-grid">
          <label className="form-field"><span>显示名称</span><input required value={draft.display_name} onChange={(event) => setDraft({ ...draft, display_name: event.target.value })} placeholder="例如：公司代理" /></label>
          <label className="form-field"><span>协议族</span><select value={draft.protocol_family} onChange={(event) => { const protocol = event.target.value as ProviderDraft['protocol_family']; const endpoint = protocol === 'anthropic' ? 'https://api.anthropic.com/v1' : protocol === 'gemini' ? 'https://generativelanguage.googleapis.com/v1beta' : 'https://api.openai.com/v1'; setDraft({ ...draft, protocol_family: protocol, ...(!provider ? { endpoint } : {}) }) }}><option value="openai_compatible">OpenAI compatible</option><option value="anthropic">Anthropic native</option><option value="gemini">Gemini native</option></select></label>
          <label className="form-field wide"><span>Endpoint</span><input required type="url" value={draft.endpoint} onChange={(event) => setDraft({ ...draft, endpoint: event.target.value })} /></label>
          <label className="form-field"><span>状态</span><select value={draft.status} disabled={!provider} onChange={(event) => setDraft({ ...draft, status: event.target.value as ProviderDraft['status'] })}><option value="active">启用</option><option value="disabled">停用</option></select></label>
          <label className="form-field wide"><span><KeyRound size={13} />API Key <em>{provider?.credential_configured ? '已配置 · 留空保持不变' : '未配置'}</em></span><input required={!provider} type="password" autoComplete="new-password" value={draft.credential} onChange={(event) => setDraft({ ...draft, credential: event.target.value })} placeholder={provider?.credential_configured ? '••••••••（不会读取旧值）' : '输入新凭据'} /></label>
        </div>
        <label className="check-row"><input type="checkbox" checked={draft.is_default} onChange={(event) => setDraft({ ...draft, is_default: event.target.checked })} /><span><strong>设为默认实例</strong><small>新项目默认使用此模型服务。</small></span></label>
        {testConnection.data ? <div className={`test-result ${testSucceeded ? '' : 'error'}`} role="status">{testSucceeded ? <CheckCircle2 size={18} /> : <AlertTriangle size={18} />}<span><strong>{testSucceeded ? '连接正常' : '连接失败'}</strong><small>{testSucceeded ? `${testConnection.data.model_count} 个模型 · ${testConnection.data.latency_millis} ms · 目录已刷新` : testConnection.data.redacted_error || `失败阶段：${testConnection.data.stage}`}</small></span></div> : null}
        {save.error || remove.error || testConnection.error ? <div className="form-error">{save.error?.message ?? remove.error?.message ?? testConnection.error?.message}</div> : null}
        <footer>{provider ? <button className="danger-button" type="button" disabled={remove.isPending} onClick={() => { if (window.confirm(`删除 ${provider.display_name}？仍被项目使用时系统会拒绝。`)) remove.mutate() }}><Trash2 size={14} />删除</button> : <button className="secondary-button" type="button" onClick={onCancel}>取消</button>}<span />{provider ? <button className="secondary-button" type="button" title="测试当前已保存的 Endpoint 与凭据" disabled={testConnection.isPending} onClick={() => testConnection.mutate()}><RefreshCw className={testConnection.isPending ? 'spin' : ''} size={14} />{testConnection.isPending ? '测试中…' : '测试连接'}</button> : null}<button className="primary-button" type="submit" disabled={save.isPending || !draft.display_name.trim() || !draft.endpoint.trim()}>{save.isPending ? '保存中…' : '保存连接'}</button></footer>
      </form>
      {provider ? <ModelSelection provider={provider} /> : null}
    </div>
  }

  function ModelSelection({ provider }: { provider: ProviderInstance }) {
    const models = useQuery({ queryKey: ['provider-models', provider.provider_id], queryFn: () => api.providerModels(provider.provider_id), retry: false })
    const [selected, setSelected] = useState<string[]>([])
    const [defaultModel, setDefaultModel] = useState('')
    const [search, setSearch] = useState('')
    const [manual, setManual] = useState('')
    useEffect(() => {
      if (!models.data) return
      setSelected(models.data.items.filter((item) => item.enabled).map((item) => item.model_id))
      setDefaultModel(models.data.items.find((item) => item.default)?.model_id || provider.default_model_id)
    }, [models.data, provider.default_model_id])
    const refresh = useMutation({ mutationFn: () => api.refreshProviderModels(provider.provider_id), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['provider-models', provider.provider_id] }) })
    const saveModels = useMutation({ mutationFn: () => api.saveProviderModels(provider.provider_id, { model_ids: selected, default_model_id: defaultModel, activate: true }), onSuccess: async () => { await Promise.all([queryClient.invalidateQueries({ queryKey: ['provider-models', provider.provider_id] }), queryClient.invalidateQueries({ queryKey: ['providers'] }), queryClient.invalidateQueries({ queryKey: ['models'] })]) } })
    const options = useMemo(() => mergeManualOptions(models.data?.items ?? [], selected).filter((item) => item.model_id.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase())), [models.data, search, selected])
    const toggle = (modelID: string) => setSelected((items) => items.includes(modelID) ? items.filter((item) => item !== modelID) : [...items, modelID])
    const addManual = () => { const modelID = manual.trim(); if (!modelID || modelID.includes('\n') || modelID.includes('\r')) return; setSelected((items) => items.includes(modelID) ? items : [...items, modelID]); setDefaultModel((value) => value || modelID); setManual('') }
    return <section className="provider-models-section">
      <header><div><span className="eyebrow">可用模型</span><h3>选择会出现在项目与会话中的模型</h3><p>读取目录不会自动启用模型；手动模型的工具能力保持“未知”。</p></div><button className="secondary-button compact" type="button" disabled={refresh.isPending} onClick={() => refresh.mutate()}><RefreshCw className={refresh.isPending ? 'spin' : ''} size={14} />从 API 读取</button></header>
      <div className="provider-model-toolbar"><label><Search size={14} /><input aria-label="搜索模型" placeholder="搜索模型" value={search} onChange={(event) => setSearch(event.target.value)} /></label><form onSubmit={(event) => { event.preventDefault(); addManual() }}><input aria-label="手动模型 ID" placeholder="手动添加模型 ID" maxLength={255} value={manual} onChange={(event) => setManual(event.target.value)} /><button type="submit" disabled={!manual.trim()}><Plus size={14} />添加</button></form></div>
      <div className="provider-model-list">{options.map((item) => <label key={item.model_id} className={selected.includes(item.model_id) ? 'selected' : ''}><input type="checkbox" checked={selected.includes(item.model_id)} onChange={() => toggle(item.model_id)} /><span><strong>{item.model_id}</strong><small>{item.source === 'manual' ? '手动添加 · 能力未知' : `${item.stale ? '缓存已过期' : 'API 目录'}${item.context_window ? ` · ${item.context_window.toLocaleString()} context` : ''}`}</small></span>{item.default ? <em>当前默认</em> : selected.includes(item.model_id) ? <Check size={14} /> : null}</label>)}</div>
      <label className="form-field model-default-select"><span>默认模型</span><select aria-label="默认模型" value={defaultModel} onChange={(event) => setDefaultModel(event.target.value)}><option value="">选择默认模型</option>{selected.map((modelID) => <option key={modelID} value={modelID}>{modelID}</option>)}</select></label>
      {models.error || refresh.error || saveModels.error ? <div className="form-error">{models.error?.message ?? refresh.error?.message ?? saveModels.error?.message}</div> : null}
      <footer><span>{selected.length} 个模型已选择</span><button className="primary-button" type="button" disabled={saveModels.isPending || selected.length === 0 || !selected.includes(defaultModel)} onClick={() => saveModels.mutate()}>{saveModels.isPending ? '保存中…' : '保存模型并启用'}</button></footer>
    </section>
  }
}

function mergeManualOptions(catalog: ProviderModelOption[], selected: string[]) {
  const byID = new Map(catalog.map((item) => [item.model_id, item]))
  for (const modelID of selected) if (!byID.has(modelID)) byID.set(modelID, { provider_id: '', model_id: modelID, source: 'manual', enabled: true, default: false, capabilities: {}, context_window: 0, max_output: 0, fetched_at: '', expires_at: '', catalog_version: '', stale: false })
  return [...byID.values()]
}
