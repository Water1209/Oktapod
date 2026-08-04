import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { CheckCircle2, CircleAlert, KeyRound, Palette, Plus, RefreshCw, Server, Trash2 } from 'lucide-react'
import { api, type ProviderInstance } from '../../shared/api/client'

type ProviderDraft = {
  display_name: string
  protocol_family: ProviderInstance['protocol_family']
  endpoint: string
  default_model_id: string
  credential: string
  status: ProviderInstance['status']
  is_default: boolean
}

const emptyDraft: ProviderDraft = {
  display_name: '',
  protocol_family: 'openai_compatible',
  endpoint: 'https://api.openai.com/v1',
  default_model_id: '',
  credential: '',
  status: 'active',
  is_default: false,
}

export function ProviderSettingsPage() {
  const queryClient = useQueryClient()
  const providers = useQuery({ queryKey: ['providers'], queryFn: api.providers })
  const [selectedId, setSelectedId] = useState<string>()
  const [creating, setCreating] = useState(false)
  const selected = providers.data?.items.find((item) => item.provider_id === selectedId)

  useEffect(() => {
    if (!creating && !selectedId && providers.data?.items[0]) setSelectedId(providers.data.items[0].provider_id)
  }, [creating, providers.data, selectedId])

  const select = (provider: ProviderInstance) => {
    setCreating(false)
    setSelectedId(provider.provider_id)
  }

  return <main className="management-page">
    <header className="management-heading">
      <div><span className="eyebrow">Settings / Providers</span><h1>模型服务</h1><p>Provider 是可独立配置的实例；协议族只决定适配方式。</p></div>
      <div className="management-heading-actions"><Link className="secondary-button compact" to="/settings/$section" params={{ section: 'appearance' }}><Palette size={14} />主题与外观</Link><button className="primary-button compact" type="button" onClick={() => { setCreating(true); setSelectedId(undefined) }}><Plus size={15} />添加实例</button></div>
    </header>
    <div className="master-detail">
      <aside className="master-list" aria-label="Provider 实例">
        {providers.isLoading ? <p className="muted-row">正在读取实例…</p> : null}
        {providers.data?.items.map((provider) => <button key={provider.provider_id} type="button" className={provider.provider_id === selectedId ? 'active' : ''} onClick={() => select(provider)}>
          <span className={`status-orb ${provider.status}`} />
          <span><strong>{provider.display_name}</strong><small>{provider.default_model_id || '尚未选择模型'}</small></span>
          {provider.is_default ? <em>默认</em> : null}
        </button>)}
        {!providers.isLoading && providers.data?.items.length === 0 ? <div className="master-empty"><Server size={20} /><strong>尚未配置</strong><span>添加 OpenAI-compatible 实例后才能发送任务。</span></div> : null}
      </aside>
      <section className="detail-panel">
        {creating ? <ProviderForm key="new" onCancel={() => setCreating(false)} /> : selected ? <ProviderForm key={selected.provider_id} provider={selected} onDeleted={() => setSelectedId(undefined)} /> : <div className="detail-empty"><Server size={28} /><h2>选择一个模型服务</h2><p>凭据只显示配置状态，旧值永不回填到浏览器。</p></div>}
      </section>
    </div>
  </main>

  function ProviderForm({ provider, onCancel, onDeleted }: { provider?: ProviderInstance; onCancel?: () => void; onDeleted?: () => void }) {
    const [draft, setDraft] = useState<ProviderDraft>(provider ? {
      display_name: provider.display_name,
      protocol_family: provider.protocol_family,
      endpoint: provider.endpoint,
      default_model_id: provider.default_model_id,
      credential: '',
      status: provider.status,
      is_default: provider.is_default,
    } : emptyDraft)
    const models = useQuery({ queryKey: ['provider-models', provider?.provider_id], queryFn: () => api.providerModels(provider!.provider_id), enabled: Boolean(provider?.provider_id), retry: false })
    const save = useMutation({
      mutationFn: () => api.saveProvider(provider?.provider_id, {
        display_name: draft.display_name.trim(),
        protocol_family: draft.protocol_family,
        endpoint: draft.endpoint.trim(),
        default_model_id: draft.default_model_id.trim(),
        credential: draft.credential || undefined,
        status: draft.status,
        is_default: draft.is_default,
      }),
      onSuccess: async (item) => {
        await queryClient.invalidateQueries({ queryKey: ['providers'] })
        setCreating(false)
        setSelectedId(item.provider_id)
      },
    })
    const test = useMutation({ mutationFn: () => api.testProvider(provider!.provider_id), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['provider-models', provider?.provider_id] }) })
    const remove = useMutation({ mutationFn: () => api.deleteProvider(provider!.provider_id), onSuccess: async () => { await queryClient.invalidateQueries({ queryKey: ['providers'] }); onDeleted?.() } })

    return <form className="provider-form" onSubmit={(event) => { event.preventDefault(); save.mutate() }}>
      <header><div><span className="eyebrow">{provider ? 'Provider instance' : 'New instance'}</span><h2>{provider?.display_name || '添加模型服务'}</h2></div>{provider ? <span className={`connection-badge ${provider.status}`}>{provider.status === 'active' ? '已启用' : provider.status === 'error' ? '异常' : '已停用'}</span> : null}</header>
      <div className="form-grid">
        <label className="form-field"><span>显示名称</span><input required value={draft.display_name} onChange={(event) => setDraft({ ...draft, display_name: event.target.value })} placeholder="例如：公司代理" /></label>
        <label className="form-field"><span>协议族</span><select value={draft.protocol_family} onChange={(event) => {
          const protocol = event.target.value as ProviderDraft['protocol_family']
          const defaultEndpoint = protocol === 'anthropic' ? 'https://api.anthropic.com/v1' : protocol === 'gemini' ? 'https://generativelanguage.googleapis.com/v1beta' : 'https://api.openai.com/v1'
          setDraft({ ...draft, protocol_family: protocol, ...(!provider ? { endpoint: defaultEndpoint } : {}) })
        }}><option value="openai_compatible">OpenAI compatible</option><option value="anthropic">Anthropic native</option><option value="gemini">Gemini native</option></select></label>
        <label className="form-field wide"><span>Endpoint</span><input required type="url" value={draft.endpoint} onChange={(event) => setDraft({ ...draft, endpoint: event.target.value })} /></label>
        <label className="form-field"><span>默认模型</span><input list="provider-model-options" value={draft.default_model_id} onChange={(event) => setDraft({ ...draft, default_model_id: event.target.value })} placeholder="model-id" /><datalist id="provider-model-options">{models.data?.items.map((item) => <option key={item.model_id} value={item.model_id} />)}</datalist></label>
        <label className="form-field"><span>状态</span><select value={draft.status} onChange={(event) => setDraft({ ...draft, status: event.target.value as ProviderDraft['status'] })}><option value="active">启用</option><option value="disabled">停用</option></select></label>
        <label className="form-field wide"><span><KeyRound size={13} />API Key <em>{provider?.credential_configured ? '已配置 · 留空保持不变' : '未配置'}</em></span><input type="password" autoComplete="new-password" value={draft.credential} onChange={(event) => setDraft({ ...draft, credential: event.target.value })} placeholder={provider?.credential_configured ? '••••••••（不会读取旧值）' : '输入新凭据'} /></label>
      </div>
      <label className="check-row"><input type="checkbox" checked={draft.is_default} onChange={(event) => setDraft({ ...draft, is_default: event.target.checked })} /><span><strong>设为默认实例</strong><small>只影响下一次 Turn 或新 Run。</small></span></label>
      {test.data ? <div className={`test-result ${test.data.redacted_error ? 'error' : 'success'}`}>{test.data.redacted_error ? <CircleAlert size={16} /> : <CheckCircle2 size={16} />}<span><strong>{test.data.stage}</strong><small>{test.data.latency_millis} ms · {test.data.model_count} 个模型 · {test.data.redacted_error || test.data.catalog_status}</small></span></div> : null}
      {save.error || test.error || remove.error ? <div className="form-error">{save.error?.message ?? test.error?.message ?? remove.error?.message}</div> : null}
      <footer>
        {provider ? <button className="danger-button" type="button" disabled={remove.isPending} onClick={() => { if (window.confirm(`删除 ${provider.display_name}？凭据也会从 SecretStore 删除。`)) remove.mutate() }}><Trash2 size={14} />删除</button> : <button className="secondary-button" type="button" onClick={onCancel}>取消</button>}
        <span />
        {provider ? <button className="secondary-button" type="button" disabled={test.isPending} onClick={() => test.mutate()}><RefreshCw className={test.isPending ? 'spin' : ''} size={14} />测试连接</button> : null}
        <button className="primary-button" type="submit" disabled={save.isPending || !draft.display_name.trim() || !draft.endpoint.trim()}>{save.isPending ? '保存中…' : '保存实例'}</button>
      </footer>
    </form>
  }
}
