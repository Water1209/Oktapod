import { useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Bot, Braces, Cable, CheckCircle2, Code2, PackageOpen, Plus, Puzzle, ShieldCheck, Upload, X, type LucideIcon } from 'lucide-react'
import { api, type Capability, type CapabilityKind, type CapabilityScope } from '../../shared/api/client'

const builtinTools = [
  'read_file', 'list_files', 'find_files', 'grep', 'edit_file', 'write_file', 'run_command',
  'git_status', 'git_diff', 'git_log', 'git_conflicts', 'git_branch', 'git_commit',
  'git_commit_paths', 'git_restore', 'git_restore_hunk',
].map((name) => ({
  id: `builtin-tool-${name}`,
  kind: 'plugin',
  name,
  version: 'builtin',
  status: 'enabled',
  risk_level: name.startsWith('git_') && !['git_status', 'git_diff', 'git_log', 'git_conflicts'].includes(name) ? 'L2' : name === 'run_command' ? 'L2' : 'L0/L1',
  source_filename: 'Oktapod Core Tool Registry',
  scope_type: 'global',
  manifest_json: '',
  permission_diff: name === 'run_command' ? ['command'] : name.startsWith('git_') ? ['git'] : ['workspace'],
  content_hash: '由 Run Snapshot 冻结',
} as unknown as Capability))

type ResourceFilter = 'all' | 'skill' | 'builtin' | 'plugin' | 'mcp' | 'agent'
type InstallType = Exclude<ResourceFilter, 'all' | 'builtin'>

export function ResourcesPage() {
  const queryClient = useQueryClient()
  const projects = useQuery({ queryKey: ['projects'], queryFn: api.projects })
  const [projectId, setProjectId] = useState('')
  const resolvedProjectId = projectId || projects.data?.items[0]?.id || ''
  const capabilities = useQuery({ queryKey: ['capabilities', resolvedProjectId], queryFn: () => api.capabilities(resolvedProjectId), enabled: Boolean(resolvedProjectId) })
  const agents = useQuery({ queryKey: ['agents', resolvedProjectId], queryFn: () => api.agents(resolvedProjectId), enabled: Boolean(resolvedProjectId) })
  const items = useMemo(() => [
    ...builtinTools,
    ...(capabilities.data?.items ?? []),
    ...(agents.data?.items ?? []).map((agent) => ({ ...agent, kind: 'agent', risk_level: 'L0', source_filename: '项目 Agent 定义', scope_type: 'project', manifest_json: agent.config_json, permission_diff: [] } as unknown as Capability)),
  ], [agents.data, capabilities.data])
  const [kind, setKind] = useState<ResourceFilter>('all')
  const filtered = items.filter((item) => {
    if (kind === 'all') return true
    if (kind === 'builtin') return item.id.startsWith('builtin-tool-')
    if (kind === 'plugin') return item.kind === 'plugin' && !item.id.startsWith('builtin-tool-')
    return item.kind === kind
  })
  const [selectedId, setSelectedId] = useState<string>()
  const selected = filtered.find((item) => item.id === selectedId) ?? filtered[0]
  const [installOpen, setInstallOpen] = useState(false)
  const [installNotice, setInstallNotice] = useState('')
  const review = useMutation({
    mutationFn: (capability: Capability) => api.reviewCapability(capability.id, 'approved', '资源中心人工核对后启用'),
    onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['capabilities', resolvedProjectId] }),
  })

  const resourceTypes: Array<[ResourceFilter, LucideIcon, string]> = [
    ['all', PackageOpen, '全部'],
    ['skill', Braces, 'Skills'],
    ['builtin', Code2, '内置 Tools'],
    ['plugin', Puzzle, 'Packages'],
    ['mcp', Cable, 'MCP'],
    ['agent', Bot, 'Agents'],
  ]

  return <main className="management-page">
    <header className="management-heading">
      <div><span className="eyebrow">Resources</span><h1>资源中心</h1><p>全局资源对所有项目可用；项目资源只进入当前项目的 Run 快照。</p></div>
      <div className="resource-heading-actions">
        <label className="inline-select"><span>项目</span><select value={resolvedProjectId} onChange={(event) => { setProjectId(event.target.value); setSelectedId(undefined) }}>{projects.data?.items.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</select></label>
        <button className="primary-button" type="button" disabled={!resolvedProjectId} onClick={() => setInstallOpen(true)}><Plus size={14} />安装资源</button>
      </div>
    </header>
    <div className="resource-explainer">
      <span><Code2 size={15} /><strong>Tools</strong> 是 Oktapod 内建动作，不需要安装。</span>
      <span><Puzzle size={15} /><strong>Packages</strong> 是可安装扩展包，可携带 Skill 或其他能力。</span>
      <span><Cable size={15} /><strong>MCP</strong> 是外部工具协议，安装连接定义后仍需人工审核和连接。</span>
    </div>
    {installNotice ? <div className="resource-install-notice" role="status"><CheckCircle2 size={16} />{installNotice}<button type="button" aria-label="关闭安装结果" onClick={() => setInstallNotice('')}><X size={14} /></button></div> : null}
    <div className="resource-layout">
      <nav className="resource-types" aria-label="资源类型">{resourceTypes.map(([value, Icon, label]) => <button type="button" className={kind === value ? 'active' : ''} key={value} onClick={() => { setKind(value); setSelectedId(undefined) }}><Icon size={15} />{label}</button>)}</nav>
      <section className="resource-list">
        {filtered.map((item) => <button type="button" className={item.id === selected?.id ? 'active' : ''} key={item.id} onClick={() => setSelectedId(item.id)}><span className={`resource-kind ${item.kind}`}>{item.id.startsWith('builtin-tool-') ? 'T' : item.kind.slice(0, 1).toUpperCase()}</span><span><strong>{item.name}</strong><small>{item.id.startsWith('builtin-tool-') ? '内置 Tool' : item.kind} · {item.version}</small></span><em className={item.status}>{item.status}</em></button>)}
        {!resolvedProjectId ? <div className="master-empty"><PackageOpen size={20} /><strong>先创建项目</strong><span>项目资源会在这里统一呈现。</span></div> : null}
        {resolvedProjectId && !filtered.length ? <div className="master-empty"><PackageOpen size={20} /><strong>还没有这类资源</strong><span>点击“安装资源”导入外部定义。</span></div> : null}
      </section>
      <section className="resource-detail">{selected ? <>
        <header><span className={`resource-kind ${selected.kind}`}>{selected.id.startsWith('builtin-tool-') ? 'T' : selected.kind.slice(0, 1).toUpperCase()}</span><div><h2>{selected.name}</h2><p>{selected.id.startsWith('builtin-tool-') ? '内置 Tool' : selected.kind} · v{selected.version}</p></div></header>
        <dl><div><dt>来源</dt><dd>{selected.source_filename || '项目定义'}</dd></div><div><dt>范围</dt><dd>{selected.scope_type === 'global' ? '全局 · 所有项目可用' : '项目 · 仅当前项目'}</dd></div><div><dt>风险</dt><dd>{selected.risk_level || 'L0'}</dd></div><div><dt>状态</dt><dd>{selected.status}</dd></div><div><dt>内容 Hash</dt><dd><code>{selected.content_hash || '由运行快照冻结'}</code></dd></div></dl>
        <div className="trust-note"><ShieldCheck size={16} /><span><strong>Trust 与权限</strong><small>{selected.permission_diff?.length ? selected.permission_diff.join('、') : '未声明额外扩权；运行时 Policy 仍会逐次检查。'}</small></span></div>
        {selected.status === 'quarantined' && !selected.id.startsWith('builtin-tool-') ? <button className="primary-button resource-review-button" type="button" disabled={review.isPending} onClick={() => review.mutate(selected)}><CheckCircle2 size={14} />审核并启用</button> : null}
        {review.error ? <p className="form-error">{review.error.message}</p> : null}
      </> : <div className="detail-empty"><PackageOpen size={28} /><h2>选择资源查看详情</h2></div>}</section>
    </div>
    {installOpen ? <ResourceInstallDialog projectId={resolvedProjectId} onClose={() => setInstallOpen(false)} onInstalled={async (item, notice) => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['capabilities', resolvedProjectId] }),
        queryClient.invalidateQueries({ queryKey: ['agents', resolvedProjectId] }),
      ])
      setKind(item.kind === 'agent' ? 'agent' : item.kind)
      setSelectedId(item.id)
      setInstallNotice(notice)
      setInstallOpen(false)
    }} /> : null}
  </main>
}

type InstallValidation = {
  fingerprint: string
  outcome: 'new' | 'same_version' | 'upgrade'
  title: string
  details: string[]
  existingId?: string
}

function ResourceInstallDialog({ projectId, onClose, onInstalled }: { projectId: string; onClose: () => void; onInstalled: (item: Capability, notice: string) => void }) {
  const fileRef = useRef<HTMLInputElement>(null)
  const capabilities = useQuery({ queryKey: ['capabilities', projectId], queryFn: () => api.capabilities(projectId), enabled: Boolean(projectId) })
  const [type, setType] = useState<InstallType>('skill')
  const [scope, setScope] = useState<CapabilityScope>('project')
  const [file, setFile] = useState<File>()
  const [name, setName] = useState('')
  const [version, setVersion] = useState('0.1.0')
  const [manifest, setManifest] = useState('')
  const [validation, setValidation] = useState<InstallValidation>()
  const [validationError, setValidationError] = useState('')
  const buildCapabilityInput = async () => {
    let bundleBase64: string | undefined
    let manifestJSON = manifest
    if (file) {
      if (file.name.toLowerCase().endsWith('.zip')) bundleBase64 = await fileToBase64(file)
      else manifestJSON = await file.text()
    }
    if (!bundleBase64 && !manifestJSON.trim()) throw new Error('请选择 ZIP / SKILL.md / JSON 文件，或粘贴资源定义。')
    if (file && file.size > 50 * 1024 * 1024) throw new Error('压缩包超过 50 MiB 安装上限。')
    const input = {
      kind: type as CapabilityKind,
      name: name || file?.name.replace(/\.[^.]+$/, '') || type,
      version,
      manifest_json: manifestJSON || '{}',
      scope_type: scope,
      source_filename: file?.name,
      bundle_base64: bundleBase64,
    }
    return { input, fingerprint: await stableInstallFingerprint(input) }
  }
  const validateInstall = async () => {
    setValidationError('')
    try {
      if (type === 'agent') {
        const parsed = parseJSON(manifest || await file?.text() || '{}')
        if (!String(parsed.name || name).trim() || !String(parsed.instructions || parsed.prompt || '').trim()) throw new Error('Agent 定义必须包含 name 和 instructions。')
        setValidation({ fingerprint: JSON.stringify(parsed), outcome: 'new', title: '基础校验通过', details: ['Agent 仅支持当前项目范围。', '确认后由服务端再次校验并登记。'] })
        return
      }
      const prepared = await buildCapabilityInput()
      const sameName = (capabilities.data?.items ?? []).filter((item) => item.kind === type && item.name === prepared.input.name && item.scope_type === scope)
      const sameVersion = sameName.find((item) => item.version === version)
      const outcome = sameVersion ? 'same_version' : sameName.length ? 'upgrade' : 'new'
      setValidation({
        fingerprint: prepared.fingerprint,
        outcome,
        existingId: sameVersion?.id,
        title: outcome === 'same_version' ? '检测到同名同版本' : outcome === 'upgrade' ? '检测到已有其他版本' : '可以安装',
        details: [
          file?.name.toLowerCase().endsWith('.zip') ? '已完成文件类型与 50 MiB 大小预检；服务端将继续检查路径穿越、符号链接、压缩炸弹和文件数量。' : '服务端将解析 manifest / SKILL.md，并校验名称、指令、权限与工具声明。',
          outcome === 'same_version' ? '确认后服务端比较内容 Hash：内容相同直接返回现有 Skill，不会重复安装；内容不同则拒绝覆盖。' : outcome === 'upgrade' ? '新版本会与旧版本并存，不会静默覆盖；Run 只冻结创建时可用的明确版本。' : '安全敏感权限、脚本或未验证签名会进入隔离审核，不会直接启用。',
        ],
      })
    } catch (error) {
      setValidation(undefined)
      setValidationError(error instanceof Error ? error.message : '资源校验失败。')
    }
  }
  const install = useMutation({
    mutationFn: async () => {
      if (type === 'agent') {
        const parsed = parseJSON(manifest || await file?.text() || '{}')
        const agent = await api.createAgent(projectId, {
          name: String(parsed.name || name),
          version: String(parsed.version || version),
          instructions: String(parsed.instructions || parsed.prompt || ''),
          model: typeof parsed.model === 'string' ? parsed.model : undefined,
          fallback_model: typeof parsed.fallback_model === 'string' ? parsed.fallback_model : undefined,
          config_json: JSON.stringify(parsed.config ?? {}),
        })
        return { ...agent, kind: 'agent', scope_type: 'project' } as unknown as Capability
      }
      const prepared = await buildCapabilityInput()
      if (!validation || validation.fingerprint !== prepared.fingerprint) throw new Error('资源内容已变化，请重新校验。')
      return api.createCapability(projectId, prepared.input)
    },
    onSuccess: (item) => onInstalled(item, validation?.outcome === 'same_version' && validation.existingId === item.id
      ? `“${item.name}” ${item.version} 内容与现有安装一致，未创建重复副本。`
      : `“${item.name}” ${item.version} 已安装到${item.scope_type === 'global' ? '全局' : '当前项目'}；状态：${item.status}。`),
  })
  const typeHelp: Record<InstallType, string> = {
    skill: '导入 SKILL.md 或包含完整技能目录的 ZIP。带 scripts 的技能会先进入隔离审核。',
    plugin: '导入含 capability.yaml 的 ZIP 扩展包。Packages 不等同于内置 Tools。',
    mcp: '导入 MCP JSON/YAML 连接清单。安装后必须审核，再显式连接。',
    agent: '导入含 name、version、instructions 的 JSON；Agent 目前只支持项目级。',
  }
  return <div className="resource-install-overlay" role="presentation">
    <section className="resource-install-dialog" role="dialog" aria-modal="true" aria-labelledby="resource-install-title">
      <header><div><span className="eyebrow">Install</span><h2 id="resource-install-title">安装外部资源</h2></div><button type="button" aria-label="关闭安装窗口" onClick={onClose}><X size={17} /></button></header>
      <div className="resource-install-kinds">{(['skill', 'plugin', 'mcp', 'agent'] as InstallType[]).map((value) => <button type="button" className={type === value ? 'active' : ''} key={value} onClick={() => { setType(value); setValidation(undefined); if (value === 'agent') setScope('project') }}>{value === 'skill' ? 'Skill' : value === 'plugin' ? 'Package' : value.toUpperCase()}</button>)}</div>
      <p className="resource-install-help">{typeHelp[type]}</p>
      <div className="resource-scope-choice">
        <button type="button" className={scope === 'project' ? 'active' : ''} onClick={() => { setScope('project'); setValidation(undefined) }}><strong>当前项目</strong><small>只进入这个项目的资源目录</small></button>
        <button type="button" disabled={type === 'agent'} className={scope === 'global' ? 'active' : ''} onClick={() => { setScope('global'); setValidation(undefined) }}><strong>全局</strong><small>当前实例的所有项目可用</small></button>
      </div>
      <button className="resource-file-drop" type="button" onClick={() => fileRef.current?.click()}><Upload size={18} /><span><strong>{file?.name || '选择资源文件'}</strong><small>ZIP、SKILL.md、JSON 或 YAML</small></span></button>
      <input ref={fileRef} hidden type="file" accept=".zip,.md,.json,.yaml,.yml" onChange={(event) => { setFile(event.target.files?.[0]); setValidation(undefined) }} />
      <div className="resource-install-fields">
        <label><span>名称（文件未声明时使用）</span><input value={name} onChange={(event) => { setName(event.target.value); setValidation(undefined) }} placeholder={type === 'skill' ? '例如 researchpilot' : '资源名称'} /></label>
        <label><span>版本</span><input value={version} onChange={(event) => { setVersion(event.target.value); setValidation(undefined) }} /></label>
      </div>
      <label className="resource-manifest-field"><span>也可以直接粘贴 {type === 'skill' ? 'SKILL.md' : type === 'agent' ? 'Agent JSON' : 'manifest'}</span><textarea value={manifest} onChange={(event) => { setManifest(event.target.value); setValidation(undefined) }} rows={5} placeholder="选择文件后可留空" /></label>
      {validation ? <div className={`resource-install-validation ${validation.outcome}`} role="status"><strong>{validation.title}</strong>{validation.details.map((detail) => <span key={detail}>{detail}</span>)}</div> : null}
      {validationError ? <p className="form-error">{validationError}</p> : null}
      {install.error ? <p className="form-error">{install.error.message}</p> : null}
      <footer><span>安装只登记能力；执行权限仍由 Run 快照、Trust 和审批控制。</span>{validation
        ? <button className="primary-button" type="button" disabled={install.isPending} onClick={() => install.mutate()}>{install.isPending ? '正在安装…' : validation.outcome === 'same_version' ? '确认并核对内容' : '确认安装'}</button>
        : <button className="primary-button" type="button" disabled={!file && !manifest.trim()} onClick={() => void validateInstall()}>校验资源</button>}</footer>
    </section>
  </div>
}

async function stableInstallFingerprint(input: Record<string, unknown>) {
  const bytes = new TextEncoder().encode(JSON.stringify(input))
  const hash = await crypto.subtle.digest('SHA-256', bytes)
  return [...new Uint8Array(hash)].map((value) => value.toString(16).padStart(2, '0')).join('')
}

function parseJSON(value: string): Record<string, unknown> {
  try {
    return JSON.parse(value) as Record<string, unknown>
  } catch {
    throw new Error('Agent 定义必须是有效 JSON。')
  }
}

function fileToBase64(file: File) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(new Error('无法读取所选文件。'))
    reader.onload = () => resolve(String(reader.result).split(',', 2)[1] || '')
    reader.readAsDataURL(file)
  })
}
