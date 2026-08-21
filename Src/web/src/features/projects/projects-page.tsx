import { useMemo, useState } from 'react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import * as Dialog from '@radix-ui/react-dialog'
import { Activity, AlertCircle, ArrowRight, CheckCircle2, Clock3, Code2, FolderClosed, FolderOpen, FolderPlus, Gauge, LockKeyhole, Search, ShieldCheck, X } from 'lucide-react'
import { api, type ModelDescriptor, type Scene, type TaskStatus } from '../../shared/api/client'
import { backgroundInboxRefetchInterval } from '../../shared/query-refresh'
import { hasLiveTasks } from '../../shared/task-status'

export function ProjectsPage() {
  const projects = useQuery({ queryKey: ['projects'], queryFn: api.projects })
  const models = useQuery({ queryKey: ['models'], queryFn: () => api.models(), retry: false, staleTime: 60_000 })
  const providers = useQuery({ queryKey: ['providers'], queryFn: api.providers, retry: false })
  const inbox = useQuery({ queryKey: ['inbox'], queryFn: api.inbox, refetchInterval: (query) => backgroundInboxRefetchInterval(query.state.data) })
  const taskQueries = useQueries({
    queries: (projects.data?.items ?? []).map((project) => ({
      queryKey: ['tasks', project.id],
      queryFn: () => api.tasks(project.id),
      refetchInterval: (query: { state: { data?: { items?: Array<{ status: TaskStatus }> } } }) => hasLiveTasks(query.state.data?.items) ? 3_000 : false,
    })),
  })
  const [query, setQuery] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  const filtered = useMemo(() => [...(projects.data?.items ?? [])]
    .filter((project) => project.name.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()))
    .sort((left, right) => new Date(right.updated_at).getTime() - new Date(left.updated_at).getTime()), [projects.data, query])
  const latestProject = query.trim() ? undefined : filtered[0]
  const recentUsage = useQuery({ queryKey: ['project-usage', latestProject?.id], queryFn: () => api.projectUsage(latestProject!.id), enabled: Boolean(latestProject?.id), staleTime: 60_000 })
  const tasks = taskQueries.flatMap((result) => result.data?.items ?? [])
  const running = tasks.filter((task) => ['queued', 'running', 'waiting_input'].includes(task.status))
  const failed = tasks.filter((task) => ['failed', 'blocked'].includes(task.status)).slice(0, 5)
  const needsAttention = inbox.data?.items ?? []
  const hasProvider = (providers.data?.items.length ?? 0) > 0

  return (
    <main className="home-page">
      <header className="home-heading"><div><span className="privacy-label"><LockKeyhole size={13} /> 仅你可见</span><h1>工作台</h1><p>继续正在进行的工作，或从一个清晰的新任务开始。</p></div><div className="home-actions"><button className="primary-button" type="button" onClick={() => setCreateOpen(true)}><FolderPlus size={15} />新建项目</button><Link className="secondary-button" to="/projects"><FolderOpen size={15} />查看项目</Link></div></header>

      {!hasProvider && !providers.isLoading ? <Link className="home-provider-callout" to="/settings/providers"><AlertCircle size={17} /><span><strong>发送任务前需要配置模型服务</strong><small>添加 Provider 实例并测试连接；不会静默使用未知配置。</small></span><ArrowRight size={15} /></Link> : null}

      <div className="home-columns">
        <section className="home-section">
          <header><span><Activity size={14} />继续处理</span><em>{running.length}</em></header>
          {running.length ? <div className="home-list">{running.slice(0, 5).map((task) => <Link key={task.id} to="/projects/$projectId/threads/$threadId" params={{ projectId: task.project_id, threadId: task.conversation_id }} search={{ inspector: 'run', event: task.run_id }}><span className={`home-state ${task.status}`}><Activity size={13} /></span><span><strong>{task.title || task.goal}</strong><small>{projectName(task.project_id)} · {statusText(task.status)}</small></span><ArrowRight size={13} /></Link>)}</div> : <HomeEmpty icon={<CheckCircle2 />} text="没有正在运行的任务" />}
        </section>
        <section className="home-section">
          <header><span><ShieldCheck size={14} />等待你处理</span><em>{needsAttention.length}</em></header>
          {needsAttention.length ? <div className="home-list">{needsAttention.slice(0, 5).map((item) => <Link key={item.id} to={item.kind === 'approval' ? '/approvals' : '/tasks/$taskId'} params={item.kind === 'approval' ? undefined : { taskId: item.task_id }}><span className="home-state attention"><ShieldCheck size={13} /></span><span><strong>{item.title}</strong><small>{item.reason}</small></span><ArrowRight size={13} /></Link>)}</div> : <HomeEmpty icon={<CheckCircle2 />} text="目前没有待处理事项" />}
        </section>
      </div>

      <section className="home-section home-projects">
        <header><span><FolderClosed size={14} />最近项目</span><label className="project-search"><Search size={14} /><input aria-label="搜索项目" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索项目" /></label></header>
        {projects.isLoading ? <ProjectSkeleton /> : null}
        {projects.error ? <div className="inline-error">项目读取失败：{projects.error.message}</div> : null}
        {!projects.isLoading && filtered.length ? <div className="home-list project-rows">{filtered.slice(0, 7).map((project) => <Link key={project.id} to="/projects/$projectId" params={{ projectId: project.id }}><span className={`project-row-icon ${project.scene}`}><FolderClosed size={16} /></span><span><strong>{project.name}</strong><small>{project.description || (project.scene === 'code' ? '代码项目' : '通用项目')}</small></span><em><LockKeyhole size={11} />{formatRecent(project.updated_at)}</em><ArrowRight size={13} /></Link>)}</div> : null}
        {!projects.isLoading && filtered.length === 0 ? <HomeEmpty icon={<FolderPlus />} text={query ? '没有匹配的项目' : '还没有个人项目'} action={!query ? <button type="button" onClick={() => setCreateOpen(true)}>创建第一个项目</button> : undefined} /> : null}
      </section>

      {failed.length ? <section className="home-section home-failed"><header><span><AlertCircle size={14} />最近失败</span><em>{failed.length}</em></header><div className="home-list">{failed.map((task) => <Link key={task.id} to="/projects/$projectId/threads/$threadId" params={{ projectId: task.project_id, threadId: task.conversation_id }} search={{ inspector: 'run', event: task.run_id }}><span className="home-state failed"><AlertCircle size={13} /></span><span><strong>{task.title || task.goal}</strong><small>{task.error_code || '打开查看恢复建议'}</small></span><Clock3 size={13} /></Link>)}</div></section> : null}

      {recentUsage.data ? <section className="home-section home-usage"><header><span><Gauge size={14} />用量摘要</span><em>{latestProject?.name}</em></header><div className="home-usage-facts"><span><strong>{recentUsage.data.model_calls.toLocaleString()}</strong><small>模型调用</small></span><span><strong>{(recentUsage.data.input_tokens + recentUsage.data.output_tokens).toLocaleString()}</strong><small>总 Token</small></span><span><strong>{recentUsage.data.reasoning_tokens.toLocaleString()}</strong><small>Reasoning</small></span><span><strong>{recentUsage.data.latency_ms.toLocaleString()} ms</strong><small>累计延迟</small></span></div></section> : null}

      <CreateProjectDialog open={createOpen} onOpenChange={setCreateOpen} models={models.data?.items ?? []} providerDefaultModel={models.data?.default_model ?? ''} modelsError={models.error?.message} />
    </main>
  )

  function projectName(projectId: string) {
    return projects.data?.items.find((project) => project.id === projectId)?.name ?? '项目'
  }
}

export function ProjectLibraryPage() {
  const projects = useQuery({ queryKey: ['projects'], queryFn: api.projects })
  const models = useQuery({ queryKey: ['models'], queryFn: api.models, retry: false, staleTime: 60_000 })
  const [query, setQuery] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  const filtered = useMemo(() => [...(projects.data?.items ?? [])]
    .filter((project) => `${project.name} ${project.description}`.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()))
    .sort((left, right) => new Date(right.updated_at).getTime() - new Date(left.updated_at).getTime()), [projects.data, query])
  return <main className="home-page project-library-page">
    <header className="home-heading"><div><span className="eyebrow">项目库</span><h1>全部项目</h1><p>按项目组织长期上下文、文件、任务和版本。</p></div><button className="primary-button" type="button" onClick={() => setCreateOpen(true)}><FolderPlus size={15} />新建项目</button></header>
    <section className="home-section home-projects library-projects">
      <header><span><FolderClosed size={14} />个人项目 <em>{filtered.length}</em></span><label className="project-search"><Search size={14} /><input aria-label="搜索全部项目" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索名称或说明" /></label></header>
      {projects.isLoading ? <ProjectSkeleton /> : null}
      {projects.error ? <div className="inline-error">项目读取失败：{projects.error.message}</div> : null}
      {filtered.length ? <div className="project-library-grid">{filtered.map((project) => <Link key={project.id} to="/projects/$projectId" params={{ projectId: project.id }}><span className={`project-row-icon ${project.scene}`}><FolderClosed size={18} /></span><span><strong>{project.name}</strong><small>{project.description || (project.scene === 'code' ? '代码项目' : '通用项目')}</small></span><dl><div><dt>场景</dt><dd>{project.scene === 'code' ? '代码' : '通用'}</dd></div><div><dt>最近更新</dt><dd>{formatRecent(project.updated_at)}</dd></div></dl><ArrowRight size={14} /></Link>)}</div> : null}
      {!projects.isLoading && filtered.length === 0 ? <HomeEmpty icon={<FolderPlus />} text={query ? '没有匹配的项目' : '还没有个人项目'} action={!query ? <button type="button" onClick={() => setCreateOpen(true)}>创建第一个项目</button> : undefined} /> : null}
    </section>
    <CreateProjectDialog open={createOpen} onOpenChange={setCreateOpen} models={models.data?.items ?? []} providerDefaultModel={models.data?.default_model ?? ''} modelsError={models.error?.message} />
  </main>
}

function HomeEmpty({ icon, text, action }: { icon: React.ReactNode; text: string; action?: React.ReactNode }) {
  return <div className="home-empty">{icon}<span>{text}</span>{action}</div>
}

function statusText(status: string) {
  return ({ queued: '排队中', running: '正在运行', waiting_input: '等待输入' } as Record<string, string>)[status] ?? status
}

function ProjectSkeleton() {
  return <div className="project-list loading" aria-label="正在读取项目">{[1, 2, 3].map((item) => <span key={item} />)}</div>
}

function CreateProjectDialog({ open, onOpenChange, models, providerDefaultModel, modelsError }: { open: boolean; onOpenChange: (open: boolean) => void; models: ModelDescriptor[]; providerDefaultModel: string; modelsError?: string }) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [scene, setScene] = useState<Scene>('code')
  const [modelKey, setModelKey] = useState('')
  const modelOptions = useMemo(() => models.map((model) => ({ ...model, key: `${model.provider_id || ''}::${model.id}` })), [models])
  const providerDefault = modelOptions.find((model) => model.default) ?? modelOptions.find((model) => model.id === providerDefaultModel)
  const selectedModel = modelOptions.find((model) => model.key === modelKey)
  const create = useMutation({
    mutationFn: () => api.createProject({ name, description, kind: 'personal', scene, model_provider_id: selectedModel?.provider_id || providerDefault?.provider_id, default_model: selectedModel?.id }),
    onSuccess: async (project) => {
      await queryClient.invalidateQueries({ queryKey: ['projects'] })
      onOpenChange(false)
      setName('')
      setDescription('')
      setModelKey('')
      await navigate({ to: '/projects/$projectId', params: { projectId: project.id } })
    },
  })

  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content className="dialog-content create-project-dialog">
          <div className="dialog-heading">
            <div><Dialog.Title>新建个人项目</Dialog.Title><Dialog.Description>项目、任务、文件和记忆仅对当前账户可见。</Dialog.Description></div>
            <Dialog.Close asChild><button className="icon-control" type="button" aria-label="关闭" title="关闭"><X size={18} /></button></Dialog.Close>
          </div>
          <form onSubmit={(event) => { event.preventDefault(); create.mutate() }}>
            <label className="form-field"><span>项目名称</span><input aria-label="项目名称" autoFocus required maxLength={120} value={name} onChange={(event) => setName(event.target.value)} placeholder="例如：Oktapod MVP" /></label>
            <label className="form-field"><span>目标说明</span><textarea aria-label="目标说明" maxLength={2000} value={description} onChange={(event) => setDescription(event.target.value)} placeholder="这个项目要持续完成什么？" /></label>
            <fieldset className="scene-field"><legend>项目场景</legend><div className="scene-segments">
              <button className={scene === 'code' ? 'active' : ''} type="button" onClick={() => setScene('code')}><Code2 size={16} /><span><strong>代码</strong><small>仓库、命令与测试</small></span></button>
              <button className={scene === 'work' ? 'active' : ''} type="button" onClick={() => setScene('work')}><FolderClosed size={16} /><span><strong>通用</strong><small>研究、文档与协作</small></span></button>
            </div></fieldset>
            <label className="form-field"><span>默认模型 <em>仅显示已启用模型</em></span><select aria-label="默认模型" value={modelKey} onChange={(event) => setModelKey(event.target.value)}><option value="">{providerDefaultModel ? `系统默认 · ${providerDefaultModel}` : '系统默认'}</option>{modelOptions.map((model) => <option value={model.key} key={model.key}>{model.provider_name ? `${model.provider_name} · ` : ''}{model.id}</option>)}</select></label>
            {modelsError ? <div className="form-hint">模型目录暂不可用，仍可使用系统默认模型；目录恢复后会自动补充选项。</div> : null}
            {create.error ? <div className="form-error">{create.error.message}</div> : null}
            <div className="dialog-actions"><Dialog.Close asChild><button className="secondary-button" type="button">取消</button></Dialog.Close><button className="primary-button" disabled={create.isPending || !name.trim()} type="submit">{create.isPending ? '正在建立...' : '建立工作空间'}</button></div>
          </form>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

function formatRecent(value: string) {
  const date = new Date(value)
  const delta = Date.now() - date.getTime()
  if (delta < 60_000) return '刚刚'
  if (delta < 60 * 60_000) return `${Math.floor(delta / 60_000)} 分钟前`
  if (delta < 24 * 60 * 60_000) return `${Math.floor(delta / (60 * 60_000))} 小时前`
  return date.toLocaleDateString('zh-CN', { month: 'short', day: 'numeric' })
}
