import { useRef, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { Cable, Check, CheckCircle2, CircleAlert, KeyRound, Upload, X } from 'lucide-react'
import { api, type Capability, type CapabilityScope, type MCPDefinitionV1, type MCPInstallCandidate, type ResourceInstallDraft } from '../../shared/api/client'
import { availabilityText } from './resource-profile'

export type InstallType = 'skill' | 'mcp'

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
      <div className="mcp-source-guide"><div><strong>这里只填写 MCP 来源</strong><small>系统会解析版本、启动方式、工具和凭据字段；安装完成后，回到对话直接说“使用这个 MCP 完成……”即可调用。</small></div><dl><div><dt>按名称</dt><dd><code>@playwright/mcp</code></dd></div><div><dt>远端服务</dt><dd><code>https://example.com/mcp</code></dd></div><div><dt>固定包</dt><dd><code>npm:package@1.2.3</code></dd></div></dl></div>
      {draft?.candidates?.length ? <div className="mcp-candidate-list" aria-label="MCP 来源候选"><strong>选择明确来源</strong>{draft.candidates.map((candidate) => <button type="button" key={`${candidate.name}@${candidate.version}`} onClick={() => selectCandidate(candidate)}><Cable size={15} /><span><b>{candidate.title || candidate.name}</b><small>{candidate.name} · v{candidate.version}{candidate.repository_url ? ` · ${candidate.repository_url.replace(/^https?:\/\//, '')}` : ''}</small></span></button>)}</div> : null}
      {resolved ? <div className="mcp-resolved-card"><span><CheckCircle2 size={16} /></span><div><strong>{resolved.name}</strong><small>{resolved.definition.transport.type === 'streamable_http' ? '远端 Streamable HTTP' : '本地 stdio'} · v{resolved.version}</small></div><em>已解析</em></div> : null}
      {(draft?.warnings ?? []).map((warning) => <p className="resource-source-warning" key={warning}><CircleAlert size={14} />{warning}</p>)}
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