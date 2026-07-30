import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import * as Select from '@radix-ui/react-select'
import * as Dialog from '@radix-ui/react-dialog'
import { ArrowUp, Bot, Check, ChevronDown, ChevronRight, Clock3, Download, File as FileIcon, FileBox, Folder, FolderPlus, HardDrive, Infinity as InfinityIcon, LockKeyhole, MessageSquare, MoreHorizontal, PanelRight, PanelRightClose, Paperclip, Plus, Puzzle, Search, Settings2, SlidersHorizontal, Sparkles, Upload, Users, Workflow, XCircle } from 'lucide-react'
import { api, type Asset, type Capability, type CapabilityKind, type CapabilityScope, type CapabilityStatus, type MCPHealth, type Memory, type PermissionMode, type PlanItem, type PlanStatus, type ProjectActivity, type ProjectInvitation, type ProviderInstance, type Scene, type TaskMode, type TeamDefinition, type Visibility } from '../../shared/api/client'
import { AssistantContent } from '../../shared/assistant-content'
import { useBootstrap } from '../../shared/bootstrap-context'
import { TASK_RUNTIME_DEFAULTS } from '../../shared/runtime-defaults'
import { FileReviewProvider } from '../tasks/multi-file-review'
import { useFileReview } from '../tasks/file-review-context'
import { MessageTaskStream } from '../tasks/message-task-stream'
import { TaskStatusStrip } from '../tasks/task-status-strip'
import { useRunEvents } from '../runs/use-run-events'
import { isCompactCommand, parseSkillInvocation, skillCommandName } from './skill-command'
import { InspectorChanges, InspectorContext, InspectorEvents, InspectorRun, InspectorSecurity, InspectorUsage } from './project-inspector'

const statusLabels: Record<string, string> = {
  queued: '待开始', running: '运行中', waiting_approval: '等待审批', waiting_input: '等待输入', paused: '已暂停', blocked: '执行受阻', succeeded: '已完成', failed: '失败', canceled: '已取消',
}

type ComposerDraft =
  | { kind: 'schedule'; name: string; cron: string; timezone: string; goal: string }
  | { kind: 'capability'; capabilityKind: CapabilityKind; name: string; version: string; manifest: string }
  | { kind: 'agent'; name: string; version: string; instructions: string; model: string }
  | { kind: 'team'; name: string; version: string; rolesJSON: string }

export function ProjectPage() {
  return (
    <FileReviewProvider>
      <ProjectWorkspace />
    </FileReviewProvider>
  )
}

function ProjectWorkspace() {
  const bootstrap = useBootstrap()
  const fileReview = useFileReview()
  const currentUsername = bootstrap.user.username || bootstrap.user.display_name || '用户'
  const { projectId, threadId } = useParams({ strict: false }) as { projectId: string; threadId?: string }
  const { conversation: conversationSearch, inspector: inspectorSearch } = useSearch({ strict: false }) as { conversation?: string; inspector?: string; event?: string }
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const project = useQuery({ queryKey: ['project', projectId], queryFn: () => api.project(projectId) })
  const tasks = useQuery({ queryKey: ['tasks', projectId], queryFn: () => api.tasks(projectId), refetchInterval: 1_500 })
  const conversations = useQuery({ queryKey: ['conversations', projectId], queryFn: () => api.conversations(projectId), refetchInterval: 2_000 })
  const activity = useQuery({ queryKey: ['project-activity', projectId], queryFn: () => api.projectActivity(projectId), refetchInterval: 1_500 })
  const planItems = useQuery({ queryKey: ['plan-items', projectId], queryFn: () => api.planItems(projectId) })
  const schedules = useQuery({ queryKey: ['schedules', projectId], queryFn: () => api.schedules(projectId) })
  const capabilities = useQuery({ queryKey: ['capabilities', projectId], queryFn: () => api.capabilities(projectId) })
  const [memoryQuery, setMemoryQuery] = useState('')
  const memories = useQuery({ queryKey: ['memories', projectId, memoryQuery], queryFn: () => api.memories(projectId, memoryQuery) })
  const agents = useQuery({ queryKey: ['agents', projectId], queryFn: () => api.agents(projectId) })
  const teams = useQuery({ queryKey: ['teams', projectId], queryFn: () => api.teams(projectId) })
  const models = useQuery({ queryKey: ['models'], queryFn: () => api.models(), retry: false, staleTime: 60_000 })
  const providers = useQuery({ queryKey: ['providers'], queryFn: api.providers, retry: false, staleTime: 60_000 })
  const [membersOpen, setMembersOpen] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const inspectorStorageKey = `oktapod.inspector.${bootstrap.user.id}.${projectId}`
  const [managementOpen, setManagementOpen] = useState(Boolean(inspectorSearch))
  const [managementTab, setManagementTab] = useState(() => inspectorSearch || window.localStorage.getItem(inspectorStorageKey) || 'run')
  const [managementMoreOpen, setManagementMoreOpen] = useState(false)
  const activeConversationId = conversationSearch === 'new' ? undefined : threadId ?? conversationSearch ?? conversations.data?.items[0]?.id
  const messages = useQuery({ queryKey: ['conversation-messages', activeConversationId], queryFn: () => api.conversationMessages(activeConversationId ?? ''), enabled: Boolean(activeConversationId), refetchInterval: 1_500 })
  const usage = useQuery({ queryKey: ['project-usage', projectId], queryFn: () => api.projectUsage(projectId), enabled: managementOpen && managementTab === 'project_usage', staleTime: 15_000 })
  const [composerDraft, setComposerDraft] = useState<ComposerDraft | undefined>()
	const [composerPrefill, setComposerPrefill] = useState<{ value: string; nonce: number }>()
  const [pendingMessages, setPendingMessages] = useState<Awaited<ReturnType<typeof api.conversationMessages>>['items']>([])
  const [streamingAssistant, setStreamingAssistant] = useState('')
  const [streamPhase, setStreamPhase] = useState<'routing' | 'responding' | 'task' | undefined>()
  const threadRef = useRef<HTMLDivElement>(null)

  const handleComposerDraft = (draft: ComposerDraft) => {
    setComposerDraft(draft)
    setManagementOpen(true)
    setManagementTab(draft.kind === 'schedule' ? 'automation' : draft.kind === 'capability' ? 'capabilities' : 'agents')
    setManagementMoreOpen(true)
  }
	const useSkill = (skill: Capability) => {
		setManagementOpen(false)
		setComposerPrefill({ value: `/${skillCommandName(skill.name)} `, nonce: Date.now() })
	}

  const selectedConversation = conversations.data?.items.find((item) => item.id === activeConversationId)
  const conversationTasks = (tasks.data?.items ?? []).filter((task) => task.conversation_id === activeConversationId)
  const activeConversationTasks = conversationTasks.filter((task) => !['succeeded', 'failed', 'blocked', 'canceled'].includes(task.status))
  const allConversationTasks = [...activeConversationTasks, ...conversationTasks.filter((task) => ['succeeded', 'failed', 'blocked', 'canceled'].includes(task.status))]
  const inspectedTask = allConversationTasks[0]
  const inspectedRunEvents = useRunEvents(inspectedTask?.run_id, Boolean(inspectedTask && managementOpen))
  const inspectedUsage = useQuery({ queryKey: ['run-usage', inspectedTask?.run_id], queryFn: () => api.runUsage(inspectedTask!.run_id), enabled: Boolean(inspectedTask?.run_id && managementOpen && ['run', 'usage'].includes(managementTab)) })
  const inspectedArtifacts = useQuery({ queryKey: ['run-artifacts', inspectedTask?.run_id], queryFn: () => api.runArtifacts(inspectedTask!.run_id), enabled: Boolean(inspectedTask?.run_id && managementOpen && ['run', 'changes'].includes(managementTab)) })
  const inspectedApprovals = useQuery({ queryKey: ['approvals', 'all'], queryFn: () => api.approvals('all'), enabled: Boolean(inspectedTask && managementOpen && managementTab === 'security') })
  const inspectedAgentTeam = useQuery({ queryKey: ['agent-team-room', inspectedTask?.run_id], queryFn: () => api.agentTeamRoom(inspectedTask!.run_id), enabled: Boolean(inspectedTask?.run_id && inspectedTask?.mode === 'agent_team' && managementOpen && managementTab === 'run') })
  const activeConversationKey = activeConversationId ?? 'new'
  const pendingForActiveConversation = pendingMessages.filter((message) => message.conversation_id === activeConversationKey)
  const streamVisible = pendingForActiveConversation.length > 0
  const confirmedMessages = messages.data?.items ?? []
  const confirmedContents = new Set(confirmedMessages.map((message) => `${message.role}:${message.content}`))
  const visibleMessages = [...confirmedMessages, ...pendingForActiveConversation.filter((message) => !confirmedContents.has(`${message.role}:${message.content}`))]
  const selectConversation = (id: string) => {
    void navigate({ to: '/projects/$projectId/threads/$threadId', params: { projectId, threadId: id } })
    void queryClient.invalidateQueries({ queryKey: ['conversation-messages', id] })
  }
  const selectInspectorTab = (tab: string) => {
    setManagementTab(tab)
    setManagementOpen(true)
    window.localStorage.setItem(inspectorStorageKey, tab)
    updateInspectorLocation(tab, inspectedTask?.run_id)
  }
  const updateInspectorLocation = (inspector?: string, event?: string) => {
    if (threadId) {
      void navigate({ to: '/projects/$projectId/threads/$threadId', params: { projectId, threadId }, search: { inspector, event } })
      return
    }
    void navigate({ to: '/projects/$projectId', params: { projectId }, search: { conversation: conversationSearch, inspector, event } })
  }

  useEffect(() => {
	const thread = threadRef.current
	if (!thread) return
	thread.scrollTo({ top: thread.scrollHeight, behavior: streamingAssistant ? 'auto' : 'smooth' })
  }, [visibleMessages.length, streamingAssistant])

  if (project.isLoading) return <div className="empty-state">正在打开项目...</div>
  if (project.error || !project.data) return <div className="empty-state"><div className="empty-icon"><XCircle size={32}/></div><h3>项目不可用</h3><p>{project.error?.message ?? '没有找到这个项目。'}</p></div>

  return (
    <div className={`project-chat-workspace ${managementOpen ? 'drawer-open' : ''}`}>
      <main className="conversation-main">
        <header className="conversation-header">
          <div><strong>{selectedConversation?.title || '新会话'}</strong><span><LockKeyhole size={12} />个人私有</span></div>
          <div className="conversation-header-actions">
            <button className="icon-control" type="button" aria-label="新建会话" title="新建会话" onClick={() => void navigate({ to: '/projects/$projectId', params: { projectId }, search: { conversation: 'new' } })}><Plus size={17} /></button>
            {project.data.kind === 'team' ? <button className="icon-control" type="button" aria-label="项目成员" title="项目成员" onClick={() => setMembersOpen(true)}><Users size={17} /></button> : null}
            <button className="icon-control" type="button" aria-label={managementOpen ? '隐藏检查器' : '打开检查器'} title={managementOpen ? '隐藏检查器' : '打开检查器'} onClick={() => { if (managementOpen) { setManagementOpen(false); updateInspectorLocation() } else selectInspectorTab(managementTab) }}>{managementOpen ? <PanelRightClose size={18} /> : <PanelRight size={18} />}</button>
          </div>
        </header>

        <div className="message-thread" aria-live="polite" ref={threadRef}>
          {messages.isLoading ? <div className="thread-state">正在载入会话…</div> : null}
          {!messages.isLoading && visibleMessages.length === 0 && !(streamVisible && streamPhase) ? <div className="thread-welcome"><span className="thread-welcome-mark">O</span><h1>{project.data.name}</h1><p>{project.data.description || '在这里提问、讨论，或直接交给 Oktapod 执行。自动模式会判断是否需要创建任务。'}</p></div> : null}
          {visibleMessages.map((message, messageIndex) => {
            const linkedTask = message.task_id ? conversationTasks.find((task) => task.id === message.task_id) : undefined
            const taskIsActive = Boolean(linkedTask && ['queued', 'running', 'waiting_approval', 'waiting_input'].includes(linkedTask.status))
            // Codex 式：任务执行流是独立的 assistant 区块，挂在“拥有该任务的最近一条消息”之后，
            // 不占用右侧 user 气泡，也不因任务终态而消失（终态后保留为可展开摘要）。
            // 归属判断：该消息是同 task_id 的最后一条可见消息（后续不再有同任务消息）。
            const ownsTask = Boolean(linkedTask) && !visibleMessages.slice(messageIndex + 1).some((later) => later.task_id === message.task_id)
            const showTaskStream = Boolean(
              ownsTask && linkedTask && fileReview &&
              (message.role === 'assistant' || message.role === 'user'),
            )
            const conversationCommand = message.role === 'user' && !message.task_id && isCompactCommand(message.content)
            const roleLabel = conversationCommand ? `会话命令 · ${currentUsername}` : message.role === 'user' ? currentUsername : message.role === 'assistant' ? 'Oktapod' : message.role
            // 拥有任务流时拆成两个并列块：消息气泡 + 独立的左侧执行流，避免执行流被右对齐进 user 气泡。
            if (showTaskStream) {
              return <div key={message.id} className="thread-entry">
                <article className={`thread-message ${message.role}${conversationCommand ? ' conversation-command' : ''}`}>
                  <div className="message-role">{roleLabel}</div>
                  <div className="message-body">{message.role === 'assistant' ? <AssistantContent content={message.content} /> : message.content}</div>
                </article>
                <div className="thread-task-stream">
                  <MessageTaskStream taskId={message.task_id!} runId={linkedTask!.run_id ?? ''} active={taskIsActive} onRequestFileReview={(items, task) => fileReview?.open(items, task)} />
                </div>
              </div>
            }
            return <article key={message.id} className={`thread-message ${message.role}${conversationCommand ? ' conversation-command' : ''}`}>
  <div className="message-role">{roleLabel}</div>
  <div className="message-body">
    {message.role === 'assistant' ? <AssistantContent content={message.content} /> : message.content}
  </div>
</article>
          })}
          {streamVisible && streamPhase ? <article className="thread-message assistant streaming"><div className="message-role">Oktapod</div><div className="message-body">{streamingAssistant ? <><AssistantContent content={streamingAssistant} streaming /><span className="stream-caret" aria-hidden="true" /></> : <>{streamPhase === 'routing' ? '正在判断如何处理…' : streamPhase === 'task' ? '正在创建任务…' : '正在生成回复…'}<span className="stream-caret" aria-hidden="true" /></>}</div></article> : null}
        </div>

        <div className="composer-stack">
          <TaskStatusStrip activeTask={allConversationTasks[0]} conversationId={activeConversationId} />
          <TaskComposer key={`composer-${activeConversationKey}-${composerPrefill?.nonce ?? 0}`} projectId={projectId} conversationId={activeConversationId} activeTask={activeConversationTasks[0]} agents={agents.data?.items ?? []} skills={(capabilities.data?.items ?? []).filter((item) => item.kind === 'skill' && item.status === 'enabled')} providers={providers.data?.items ?? []} initialValue={composerPrefill?.value} onComposerDraft={handleComposerDraft} onConversationSelected={selectConversation} onOptimisticMessage={(content) => { setPendingMessages([{ id: `pending-${Date.now()}`, project_id: projectId, conversation_id: activeConversationKey, task_id: null, server_seq: Number.MAX_SAFE_INTEGER, role: 'user', visibility: 'private', content, created_at: new Date().toISOString() }]); setStreamingAssistant(''); setStreamPhase('routing') }} onRoute={(route) => setStreamPhase(route === 'task' ? 'task' : 'responding')} onDelta={(delta) => { setStreamPhase('responding'); setStreamingAssistant((current) => current + delta) }} onFailed={() => { setStreamingAssistant(''); setStreamPhase(undefined) }} onSettled={() => { setPendingMessages([]); setStreamingAssistant(''); setStreamPhase(undefined) }} />
        </div>
      </main>

      {managementOpen ? <aside className="project-management-panel">
        <header><div><strong>检查器</strong><span>{inspectedTask?.title || project.data.name}</span></div><button className="icon-control" type="button" aria-label="关闭检查器" title="关闭检查器" onClick={() => { setManagementOpen(false); updateInspectorLocation() }}><PanelRightClose size={17} /></button></header>
        <nav aria-label="工作台检查器">
          {([['run', 'Run'], ['context', 'Context'], ['usage', 'Usage'], ['changes', 'Changes'], ['trace', 'Trace']] as Array<[string, string]>).map(([value, label]) => <button className={managementTab === value ? 'active' : ''} type="button" key={value} onClick={() => selectInspectorTab(value)}>{label}</button>)}
          <button className={managementMoreOpen || ['tools', 'security', 'overview', 'tasks', 'assets', 'capabilities', 'memories', 'activity', 'plan', 'automation', 'agents'].includes(managementTab) ? 'active' : ''} type="button" aria-expanded={managementMoreOpen} onClick={() => setManagementMoreOpen((value) => !value)}><MoreHorizontal size={14} />更多</button>
        </nav>
        {managementMoreOpen ? <div className="panel-secondary-nav" aria-label="更多检查器与项目视图">
          {([['tools', 'Tools'], ['security', 'Security'], ['overview', '项目'], ['tasks', '任务'], ['assets', '文件'], ['capabilities', '能力'], ['memories', '记忆'], ['activity', '动态'], ['plan', '计划'], ['automation', '自动化'], ['agents', '智能体'], ['project_usage', '项目用量']] as Array<[string, string]>).map(([value, label]) => <button className={managementTab === value ? 'active' : ''} type="button" key={value} onClick={() => { selectInspectorTab(value); setManagementMoreOpen(false) }}>{label}</button>)}
        </div> : null}
        <div className="panel-content">
          {managementTab === 'run' ? <InspectorRun task={inspectedTask} events={inspectedRunEvents.data.items} usage={inspectedUsage.data} artifacts={inspectedArtifacts.data?.items ?? []} agentTeam={inspectedAgentTeam.data} /> : null}
          {managementTab === 'context' ? <InspectorContext projectModel={project.data.default_model} events={inspectedRunEvents.data.items} /> : null}
          {managementTab === 'tools' ? <InspectorEvents events={inspectedRunEvents.data.items.filter((item) => item.event_type.includes('tool'))} empty="当前 Run 尚无工具事件。" /> : null}
          {managementTab === 'security' ? <InspectorSecurity projectId={projectId} taskId={inspectedTask?.id} approvals={inspectedApprovals.data?.items ?? []} /> : null}
          {managementTab === 'changes' ? <InspectorChanges runId={inspectedTask?.run_id} artifacts={inspectedArtifacts.data?.items ?? []} /> : null}
          {managementTab === 'trace' ? <InspectorEvents events={inspectedRunEvents.data.items} empty="当前 Run 尚无事件。" /> : null}
          {managementTab === 'usage' ? <InspectorUsage usage={inspectedUsage.data} /> : null}
          {managementTab === 'overview' ? <div className="project-overview-list"><button type="button" onClick={() => setSettingsOpen(true)}><Settings2 size={16} /><span><strong>项目设置</strong><small>{project.data.description || '补充项目目标与模型策略'}</small></span><ChevronRight size={15} /></button><div><Bot size={16} /><span><strong>默认智能体</strong><small>Oktapod Generalist</small></span></div><div><Sparkles size={16} /><span><strong>模型策略</strong><small>{project.data.default_model || '由 Provider 自动选择'}</small></span></div><div><LockKeyhole size={16} /><span><strong>可见范围</strong><small>仅当前用户</small></span></div></div> : null}
          {managementTab === 'tasks' ? <TasksView tasks={tasks.data?.items ?? []} loading={tasks.isLoading} /> : null}
          {managementTab === 'activity' ? <ActivityView activity={activity.data?.items ?? []} /> : null}
          {managementTab === 'plan' ? <PlanView projectId={projectId} planItems={planItems.data?.items ?? []} /> : null}
          {managementTab === 'assets' ? <AssetsView projectId={projectId} /> : null}
          {managementTab === 'automation' ? <ScheduleView key={composerDraft?.kind === 'schedule' ? JSON.stringify(composerDraft) : 'schedule'} projectId={projectId} schedules={schedules.data?.items ?? []} draft={composerDraft?.kind === 'schedule' ? composerDraft : undefined} /> : null}
          {managementTab === 'capabilities' ? <CapabilityView key={composerDraft?.kind === 'capability' ? JSON.stringify(composerDraft) : 'capability'} projectId={projectId} capabilities={capabilities.data?.items ?? []} draft={composerDraft?.kind === 'capability' ? composerDraft : undefined} onUseSkill={useSkill} /> : null}
          {managementTab === 'memories' ? <MemoryView projectId={projectId} memories={memories.data?.items ?? []} query={memoryQuery} onQueryChange={setMemoryQuery} /> : null}
          {managementTab === 'agents' ? <><AgentView key={composerDraft?.kind === 'agent' ? JSON.stringify(composerDraft) : 'agent-default'} projectId={projectId} agents={agents.data?.items ?? []} models={models.data?.items ?? []} modelsError={models.error?.message} draft={composerDraft?.kind === 'agent' ? composerDraft : undefined} /><TeamDefinitionView key={composerDraft?.kind === 'team' ? JSON.stringify(composerDraft) : 'team-default'} projectId={projectId} teams={teams.data?.items ?? []} draft={composerDraft?.kind === 'team' ? composerDraft : undefined} /></> : null}
          {managementTab === 'project_usage' ? <UsageView usage={usage.data} loading={usage.isLoading} error={usage.error?.message} /> : null}
        </div>
      </aside> : null}

      {project.data.kind === 'team' ? <MemberDialog projectId={projectId} isTeam open={membersOpen} onOpenChange={setMembersOpen} /> : null}
      <ProjectSettingsDialog project={project.data} models={models.data?.items ?? []} providerDefaultModel={models.data?.default_model ?? ''} modelsError={models.error?.message} open={settingsOpen} onOpenChange={setSettingsOpen} />
    </div>
  )
}

function ProjectSettingsDialog({ project, models, providerDefaultModel, modelsError, open, onOpenChange }: { project: Awaited<ReturnType<typeof api.project>>; models: { id: string }[]; providerDefaultModel: string; modelsError?: string; open: boolean; onOpenChange: (open: boolean) => void }) {
  const queryClient = useQueryClient()
  const [defaultModel, setDefaultModel] = useState(project.default_model)
  const [fallbackModel, setFallbackModel] = useState(project.fallback_model)
  const [budgetLimited, setBudgetLimited] = useState(project.max_model_calls > 0 || project.max_tokens > 0)
  const [maxModelCalls, setMaxModelCalls] = useState(project.max_model_calls || TASK_RUNTIME_DEFAULTS.maxModelCalls)
  const [maxTokens, setMaxTokens] = useState(project.max_tokens || TASK_RUNTIME_DEFAULTS.maxTokens)
  const modelOptions = Array.from(new Set([providerDefaultModel, project.default_model, project.fallback_model, ...models.map((model) => model.id)].filter(Boolean)))
  const update = useMutation({
    mutationFn: () => api.updateProjectSettings(project.id, { default_model: defaultModel.trim(), fallback_model: fallbackModel.trim(), max_model_calls: budgetLimited ? maxModelCalls : 0, max_tokens: budgetLimited ? maxTokens : 0 }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['project', project.id] })
      onOpenChange(false)
    },
  })
  const budgetInvalid = budgetLimited && (maxModelCalls < 1 || maxModelCalls > 1000 || maxTokens < 1 || maxTokens > 10_000_000)
  return <Dialog.Root open={open} onOpenChange={(next) => { if (next) { setDefaultModel(project.default_model); setFallbackModel(project.fallback_model); setBudgetLimited(project.max_model_calls > 0 || project.max_tokens > 0); setMaxModelCalls(project.max_model_calls || TASK_RUNTIME_DEFAULTS.maxModelCalls); setMaxTokens(project.max_tokens || TASK_RUNTIME_DEFAULTS.maxTokens) } onOpenChange(next) }}>
    <Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><Dialog.Content className="dialog-content">
      <div className="dialog-heading"><div><Dialog.Title>项目配置</Dialog.Title><Dialog.Description>Owner 或 Approver 可更新模型策略；已创建的 Run 仍使用自己的不可变快照。</Dialog.Description></div><Dialog.Close className="icon-button" aria-label="关闭">×</Dialog.Close></div>
      <form onSubmit={(event) => { event.preventDefault(); update.mutate() }}>
        <label className="form-field"><span>默认模型</span><select aria-label="项目默认模型" value={defaultModel} onChange={(event) => setDefaultModel(event.target.value)}><option value="">{providerDefaultModel ? `系统默认 · ${providerDefaultModel}` : '系统默认'}</option>{modelOptions.map((model) => <option value={model} key={model}>{model}</option>)}</select></label>
        <label className="form-field"><span>Fallback 模型</span><select aria-label="项目 fallback 模型" value={fallbackModel} onChange={(event) => setFallbackModel(event.target.value)}><option value="">不使用 Fallback</option>{modelOptions.map((model) => <option value={model} key={model}>{model}</option>)}</select></label>
        <fieldset className="project-budget-setting">
          <legend><span>项目资源配额</span><small>用于此项目之后创建的所有任务</small></legend>
          <div className="budget-mode-switch" role="group" aria-label="项目资源配额模式">
            <button type="button" className={budgetLimited ? 'active' : ''} aria-pressed={budgetLimited} onClick={() => setBudgetLimited(true)}><SlidersHorizontal size={14} /><span><strong>受控</strong><small>限制调用次数与 Token</small></span></button>
            <button type="button" className={!budgetLimited ? 'active' : ''} aria-pressed={!budgetLimited} onClick={() => setBudgetLimited(false)}><InfinityIcon size={14} /><span><strong>不限额</strong><small>持续执行直到任务结束</small></span></button>
          </div>
          {budgetLimited ? <div className="budget-inputs"><label><span>最多模型调用</span><input aria-label="项目最多模型调用" type="number" min={1} max={1000} value={maxModelCalls} onChange={(event) => setMaxModelCalls(Number(event.target.value))} /></label><label><span>最多 Token</span><input aria-label="项目最多 Token" type="number" min={1} max={10000000} step={1} value={maxTokens} onChange={(event) => setMaxTokens(Number(event.target.value))} /></label></div> : <p>不设置调用次数和 Token 上限；权限、审批及安全规则仍然有效。</p>}
          <p className="project-budget-note">新任务会冻结此配置；已经运行或等待审批的任务不会被改写。</p>
        </fieldset>
        {modelsError ? <div className="form-hint">模型目录暂不可用，当前仍保留系统默认和项目已保存的模型。</div> : null}
        {update.error ? <p className="form-error">{update.error.message}</p> : null}
        <div className="dialog-actions"><Dialog.Close asChild><button className="secondary-button" type="button">取消</button></Dialog.Close><button className="primary-button" type="submit" disabled={update.isPending || budgetInvalid}>{update.isPending ? '保存中…' : '保存配置'}</button></div>
      </form>
    </Dialog.Content></Dialog.Portal>
  </Dialog.Root>
}

function UsageView({ usage, loading, error }: { usage?: Awaited<ReturnType<typeof api.projectUsage>>; loading: boolean; error?: string }) {
  if (loading) return <div className="work-surface-empty"><span>正在加载项目用量…</span></div>
  if (error) return <div className="task-monitor-error" role="alert">项目用量仅对 Owner/Approver 开放：{error}</div>
  if (!usage) return <WorkSurfaceEmpty icon={<HardDrive />} title="暂无项目用量" copy="模型调用完成后，这里会按日期、模型、成员和 Agent 聚合展示。" />
  return <div className="usage-surface">
    <div className="automation-intro"><HardDrive size={22} /><div><h2>项目用量与成本</h2><p>默认统计最近 30 天；仅展示聚合指标，不包含任务标题、提示词或私密正文。成本状态：{usage.cost_status}。</p></div></div>
    <div className="usage-kpi-grid">
      <div><span>模型调用</span><strong>{usage.model_calls}</strong></div>
      <div><span>输入 / 输出 Token</span><strong>{usage.input_tokens} / {usage.output_tokens}</strong></div>
      <div><span>总耗时</span><strong>{usage.latency_ms} ms</strong></div>
      <div><span>费用</span><strong>{usage.cost_decimal} {usage.currency}</strong></div>
    </div>
    <UsageBucketList title="按模型" items={usage.by_model} />
    <UsageBucketList title="按成员" items={usage.by_member} />
    <UsageBucketList title="按 Agent" items={usage.by_agent} />
    <UsageBucketList title="按日期" items={usage.by_day} />
  </div>
}

function UsageBucketList({ title, items }: { title: string; items: Awaited<ReturnType<typeof api.projectUsage>>['by_day'] }) {
  return <section className="usage-breakdown"><div className="section-heading"><h3>{title}</h3><small>{items.length} 个分组</small></div>{items.length === 0 ? <p className="form-hint">暂无数据</p> : <div className="usage-table" role="table" aria-label={title}>{items.map((item) => <div className="usage-row" role="row" key={item.key}><strong>{item.label}</strong><span>{item.model_calls} 次 · {item.input_tokens + item.output_tokens} Token · {item.cost_decimal}</span></div>)}</div>}</section>
}

function MemberDialog({ projectId, isTeam, open, onOpenChange }: { projectId: string; isTeam: boolean; open: boolean; onOpenChange: (open: boolean) => void }) {
  const [email, setEmail] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [role, setRole] = useState<'owner' | 'member' | 'approver'>('member')
  const [inviteToken, setInviteToken] = useState('')
  const queryClient = useQueryClient()
  const members = useQuery({ queryKey: ['project-members', projectId], queryFn: () => api.projectMembers(projectId), enabled: open })
  const invitations = useQuery({ queryKey: ['project-invitations', projectId], queryFn: () => api.projectInvitations(projectId), enabled: open && isTeam })
  const add = useMutation({
    mutationFn: () => api.createProjectInvitation(projectId, { email, display_name: displayName, role }),
    onSuccess: async (created) => {
      setEmail('')
      setDisplayName('')
      setInviteToken(created.invite_token ?? '')
      await queryClient.invalidateQueries({ queryKey: ['project-invitations', projectId] })
    },
  })
  const remove = useMutation({
    mutationFn: (userId: string) => api.removeProjectMember(projectId, userId),
    onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['project-members', projectId] }),
  })
  const revoke = useMutation({
    mutationFn: (invitationId: string) => api.revokeProjectInvitation(invitationId),
    onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['project-invitations', projectId] }),
  })
  return <Dialog.Root open={open} onOpenChange={onOpenChange}><Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><Dialog.Content className="dialog-content member-dialog"><div className="dialog-heading"><div><Dialog.Title>{isTeam ? '团队项目成员' : 'Personal 项目成员范围'}</Dialog.Title><Dialog.Description>{isTeam ? '创建一次性邀请。收件人使用邀请令牌接受后才加入项目；加入后可见 project 历史，private 与 selected 内容不会暴露。移除后新请求立即失权。' : 'Personal 项目固定为本地用户。创建 Team Project 后可邀请成员。'}</Dialog.Description></div><Dialog.Close className="icon-button" aria-label="关闭">×</Dialog.Close></div>
    {isTeam ? <form className="member-invite-form" onSubmit={(event) => { event.preventDefault(); if (email && displayName) add.mutate() }}><label><span>邮箱</span><input aria-label="成员邮箱" type="email" required value={email} onChange={(event) => setEmail(event.target.value)} placeholder="name@example.com" /></label><label><span>显示名</span><input aria-label="成员显示名" required value={displayName} onChange={(event) => setDisplayName(event.target.value)} /></label><label><span>角色</span><select aria-label="成员角色" value={role} onChange={(event) => setRole(event.target.value as typeof role)}><option value="member">Member</option><option value="approver">Approver</option><option value="owner">Owner</option></select></label><button type="submit" disabled={add.isPending || !email || !displayName}>创建邀请</button></form> : null}
    {add.error || remove.error ? <div className="form-error">{add.error?.message ?? remove.error?.message}</div> : null}
    {inviteToken ? <div className="invite-token" role="status"><strong>一次性邀请令牌（仅显示本次）</strong><code>{inviteToken}</code><small>请通过受控渠道发送给收件人；服务端只保存哈希。</small></div> : null}
    {isTeam && invitations.data?.items.length ? <div className="invitation-list"><h3>邀请记录</h3>{invitations.data.items.map((invitation) => <InvitationRow invitation={invitation} onRevoke={() => revoke.mutate(invitation.id)} key={invitation.id} />)}</div> : null}
    <div className="member-list">{members.isLoading ? <p>正在读取成员…</p> : members.data?.items.map((member) => <div key={member.user_id}><span className="member-avatar">{member.display_name.slice(0, 1).toUpperCase()}</span><span><strong>{member.display_name}</strong><small>{member.email || '本地账户'} · {member.role}</small></span>{isTeam && member.role !== 'owner' ? <button type="button" disabled={remove.isPending} onClick={() => remove.mutate(member.user_id)}>移除</button> : <em>{member.role === 'owner' ? 'Owner' : ''}</em>}</div>)}</div>
  </Dialog.Content></Dialog.Portal></Dialog.Root>
}

function InvitationRow({ invitation, onRevoke }: { invitation: ProjectInvitation; onRevoke: () => void }) {
  return <div className="invitation-row"><span><strong>{invitation.display_name}</strong><small>{invitation.email} · {invitation.role} · {invitation.status}</small></span>{invitation.status === 'pending' ? <button type="button" onClick={onRevoke}>撤销邀请</button> : null}</div>
}

function ActivityView({ activity }: { activity: ProjectActivity[] }) {
  if (activity.length === 0) return <WorkSurfaceEmpty icon={<MessageSquare />} title="还没有项目动态" copy="发起第一个任务后，已提交的状态、审批、工具和结果事件会出现在这里。" />
  return <div className="timeline-list">{activity.map((event) => <Link to="/tasks/$taskId" params={{ taskId: event.task_id }} className="timeline-row" key={event.id}><span className={`status-dot ${event.task_status}`} /><span><strong>{event.task_title}</strong><small>{activityLabel(event.event_type)} · {new Date(event.occurred_at).toLocaleString('zh-CN')}</small></span></Link>)}</div>
}

function activityLabel(eventType: string) {
  const labels: Record<string, string> = {
    'run.created': '任务已创建', 'run.paused': '任务已暂停', 'run.resumed': '任务已继续', 'run.canceled': '任务已取消',
    'step.started': '智能体开始执行', 'step.recovered': '任务已从检查点恢复', 'step.succeeded': '步骤已完成',
    'step.failed': '步骤执行失败', 'step.blocked': '步骤需要处理', 'checkpoint.committed': '安全检查点已保存',
    'approval.requested': '风险动作等待审批', 'approval.decided': '审批决定已保存', 'tool.planned': '工具计划已校验',
    'tool.execution_started': '工具开始执行', 'tool.completed': '工具执行完成', 'validation.completed': '确定性验收完成',
    'artifact.created': '结果资产已生成', 'outcome.accepted': '成果已接受',
  }
  return labels[eventType] ?? eventType
}

function PlanView({ projectId, planItems }: { projectId: string; planItems: PlanItem[] }) {
  const [title, setTitle] = useState('')
  const [draggedID, setDraggedID] = useState<string | null>(null)
  const queryClient = useQueryClient()
  const create = useMutation({
    mutationFn: () => api.createPlanItem(projectId, { title, visibility: 'private' }),
    onSuccess: async () => {
      setTitle('')
      await queryClient.invalidateQueries({ queryKey: ['plan-items', projectId] })
    },
  })
  const move = useMutation({
    mutationFn: ({ id, status }: { id: string; status: PlanStatus }) => api.movePlanItem(id, status),
    onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['plan-items', projectId] }),
  })
  const columns = [
    ['待开始', 'todo', planItems.filter((item) => item.status === 'todo')],
    ['进行中', 'in_progress', planItems.filter((item) => item.status === 'in_progress')],
    ['暂停', 'paused', planItems.filter((item) => item.status === 'paused')],
    ['完成', 'done', planItems.filter((item) => item.status === 'done')],
  ] as const
  return <>
    <form className="plan-toolbar" onSubmit={(event) => { event.preventDefault(); if (title.trim()) create.mutate() }}><label><span className="sr-only">待办标题</span><input value={title} onChange={(event) => setTitle(event.target.value)} placeholder="添加独立待办，不改变任务运行状态" /></label><button type="submit" disabled={!title.trim() || create.isPending}>新建待办</button>{create.error ? <small>{create.error.message}</small> : null}</form>
    <div className="plan-board">{columns.map(([columnTitle, status, items]) => <section className="plan-column" data-status={status} key={columnTitle} onDragOver={(event) => { event.preventDefault(); event.dataTransfer.dropEffect = 'move' }} onDrop={(event) => { event.preventDefault(); const itemID = event.dataTransfer.getData('text/plain') || draggedID; const item = planItems.find((candidate) => candidate.id === itemID); if (itemID && item && item.status !== status) move.mutate({ id: itemID, status }); setDraggedID(null) }}><h3>{columnTitle}<span>{items.length}</span></h3>{items.map((item) => <article key={item.id} className={`plan-card${draggedID === item.id ? ' is-dragging' : ''}`} draggable onDragStart={(event) => { setDraggedID(item.id); event.dataTransfer.effectAllowed = 'move'; event.dataTransfer.setData('text/plain', item.id) }} onDragEnd={() => setDraggedID(null)}><strong>{item.title}</strong><small>{item.source === 'task' ? '关联任务' : '手工待办'} · {item.visibility === 'private' ? '仅自己' : '项目成员'}</small><div className="plan-card-actions">{item.linked_task_id ? <Link to="/tasks/$taskId" params={{ taskId: item.linked_task_id }}>查看任务</Link> : <span />}
      <label><span className="sr-only">移动计划项</span><select aria-label={`移动 ${item.title}`} value={item.status} disabled={move.isPending} onChange={(event) => move.mutate({ id: item.id, status: event.target.value as PlanStatus })}>{columns.map(([label, value]) => <option value={value} key={value}>{label}</option>)}</select></label></div></article>)}{items.length === 0 ? <p>暂无事项</p> : null}</section>)}</div>
  </>
}

function ScheduleView({ projectId, schedules, draft }: { projectId: string; schedules: Awaited<ReturnType<typeof api.schedules>>['items']; draft?: Extract<ComposerDraft, { kind: 'schedule' }> }) {
  const queryClient = useQueryClient()
  const [name, setName] = useState(draft?.name ?? '')
  const [cron, setCron] = useState(draft?.cron ?? '0 9 * * *')
  const [timezone, setTimezone] = useState(draft?.timezone ?? 'UTC')
  const [goal, setGoal] = useState(draft?.goal ?? '')
  const [misfirePolicy, setMisfirePolicy] = useState<'skip' | 'run_now' | 'ask'>('ask')
  const [overlapPolicy, setOverlapPolicy] = useState<'forbid' | 'queue' | 'replace'>('forbid')
  const create = useMutation({
    mutationFn: () => api.createSchedule(projectId, { name, cron_expression: cron, timezone, goal, mode: 'single_agent', visibility: 'private', misfire_policy: misfirePolicy, overlap_policy: overlapPolicy, misfire_grace_seconds: 300 }),
    onSuccess: async () => { setName(''); setGoal(''); await queryClient.invalidateQueries({ queryKey: ['schedules', projectId] }) },
  })
  const toggle = useMutation({ mutationFn: ({ id, status }: { id: string; status: 'enabled' | 'disabled' }) => api.setScheduleStatus(id, status), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['schedules', projectId] }) })
  return <div className="automation-surface"><div className="automation-intro"><Clock3 size={22} /><div><h2>自动化计划</h2><p>计划任务持久化到服务端，重启后按时区继续；每次触发都会创建普通任务并进入统一监控。对话草稿格式：<code>创建计划：名称 | Cron | IANA 时区 | 目标</code>。</p></div></div>
    <form className="schedule-form" onSubmit={(event) => { event.preventDefault(); if (name.trim() && goal.trim()) create.mutate() }}><input aria-label="计划名称" value={name} onChange={(event) => setName(event.target.value)} placeholder="计划名称" required /><input aria-label="Cron 表达式" value={cron} onChange={(event) => setCron(event.target.value)} placeholder="Cron，例如 0 9 * * *" required /><input aria-label="时区" value={timezone} onChange={(event) => setTimezone(event.target.value)} placeholder="IANA 时区，例如 Asia/Shanghai" required /><input aria-label="计划目标" value={goal} onChange={(event) => setGoal(event.target.value)} placeholder="触发后要完成的目标" required /><label><span>错过触发</span><select aria-label="错过触发策略" value={misfirePolicy} onChange={(event) => setMisfirePolicy(event.target.value as typeof misfirePolicy)}><option value="ask">待人工确认</option><option value="run_now">立即补跑</option><option value="skip">跳过</option></select></label><label><span>重叠执行</span><select aria-label="重叠执行策略" value={overlapPolicy} onChange={(event) => setOverlapPolicy(event.target.value as typeof overlapPolicy)}><option value="forbid">禁止并发</option><option value="queue">排队</option><option value="replace">替换旧任务</option></select></label><button type="submit" disabled={create.isPending || !name.trim() || !goal.trim()}>新建计划</button></form>
    {create.error ? <div className="form-error">{create.error.message}</div> : null}<div className="schedule-list">{schedules.map((schedule) => <article className="schedule-card" key={schedule.id}><div><strong>{schedule.name}</strong><small>{schedule.cron_expression} · {schedule.timezone} · 下次 {new Date(schedule.next_run_at).toLocaleString('zh-CN')}</small><p>{schedule.goal}</p><small>错过：{schedule.misfire_policy} · 重叠：{schedule.overlap_policy} · 宽限 {schedule.misfire_grace_seconds}s</small></div><button type="button" onClick={() => toggle.mutate({ id: schedule.id, status: schedule.status === 'enabled' ? 'disabled' : 'enabled' })} disabled={toggle.isPending}>{schedule.status === 'enabled' ? '停用' : '启用'}</button></article>)}{schedules.length === 0 ? <WorkSurfaceEmpty icon={<Clock3 />} title="还没有计划任务" copy="创建一个按时区执行的自动化任务。" /> : null}</div>
  </div>
}

function CapabilityView({ projectId, capabilities, draft, onUseSkill }: { projectId: string; capabilities: Awaited<ReturnType<typeof api.capabilities>>['items']; draft?: Extract<ComposerDraft, { kind: 'capability' }>; onUseSkill: (skill: Capability) => void }) {
  const queryClient = useQueryClient()
  const [name, setName] = useState(draft?.name ?? '')
  const [version, setVersion] = useState(draft?.version ?? '0.1.0')
  const [kind, setKind] = useState<CapabilityKind>(draft?.capabilityKind ?? 'skill')
  const [scope, setScope] = useState<CapabilityScope>('global')
  const [scopeFilter, setScopeFilter] = useState<'all' | CapabilityScope>('all')
  const [manifest, setManifest] = useState(draft?.manifest ?? '{"description":"","triggers":[],"instructions":""}')
  const [bundleBase64, setBundleBase64] = useState('')
  const [sourceFilename, setSourceFilename] = useState('')
  const [importError, setImportError] = useState('')
  const manifestFileRef = useRef<HTMLInputElement>(null)
  const [mcpHealth, setMcpHealth] = useState<Record<string, MCPHealth>>({})
  const create = useMutation({ mutationFn: () => api.createCapability(projectId, { kind, name, version, manifest_json: manifest, scope_type: scope, source_filename: sourceFilename || undefined, bundle_base64: bundleBase64 || undefined }), onSuccess: async () => { setName(''); setVersion('0.1.0'); setBundleBase64(''); setSourceFilename(''); await queryClient.invalidateQueries({ queryKey: ['capabilities', projectId] }) } })
  const toggle = useMutation({ mutationFn: ({ id, status }: { id: string; status: 'enabled' | 'disabled' }) => api.setCapabilityStatus(id, status), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['capabilities', projectId] }) })
  const review = useMutation({ mutationFn: ({ id, decision }: { id: string; decision: 'approved' | 'rejected' }) => api.reviewCapability(id, decision, decision === 'approved' ? '项目 Owner 已审查 manifest 权限' : '项目 Owner 拒绝该能力'), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['capabilities', projectId] }) })
  const connect = useMutation({ mutationFn: (id: string) => api.connectMCP(id), onSuccess: (health) => setMcpHealth((current) => ({ ...current, [health.capability_id]: health })) })
  const disconnect = useMutation({ mutationFn: (id: string) => api.disconnectMCP(id), onSuccess: (health) => setMcpHealth((current) => ({ ...current, [health.capability_id]: health })) })
  const importManifestFile = async (file: File) => {
    try {
      if (/\.zip$/i.test(file.name)) {
        setKind('skill')
        setName(file.name.replace(/\.zip$/i, ''))
        setVersion('0.1.0')
        setManifest('{}')
        setBundleBase64(await fileToBase64(file))
        setSourceFilename(file.name)
        setImportError('')
        return
      }
      const text = await file.text()
      setBundleBase64('')
      setSourceFilename(file.name)
      if (/\.md$/i.test(file.name)) {
        const frontmatterName = text.match(/^name:\s*["']?([^\r\n"']+)/m)?.[1]?.trim()
        setKind('skill')
        setName(frontmatterName || file.name.replace(/\.md$/i, ''))
        setVersion('0.1.0')
        setManifest(text)
        setImportError('')
        return
      }
      const parsed = JSON.parse(text) as { kind?: CapabilityKind; name?: string; version?: string; manifest_json?: string } | Record<string, unknown>
      const envelope = parsed as { kind?: CapabilityKind; name?: string; version?: string; manifest_json?: string }
      setKind(envelope.kind && ['skill', 'plugin', 'mcp', 'agent'].includes(envelope.kind) ? envelope.kind : 'skill')
      setName(envelope.name ?? file.name.replace(/\.json$/i, ''))
      setVersion(envelope.version ?? '0.1.0')
      setManifest(typeof envelope.manifest_json === 'string' ? envelope.manifest_json : JSON.stringify(parsed, null, 2))
      setImportError('')
    } catch {
      setImportError('只支持 ZIP、合法 JSON manifest，或带 YAML frontmatter 的 SKILL.md；所有文件都会经过服务端预检。')
    }
  }
  const visibleCapabilities = capabilities.filter((capability) => scopeFilter === 'all' || capability.scope_type === scopeFilter)
  return <div className="capability-surface">
    <div className="automation-intro"><Puzzle size={22} /><div><h2>能力中心</h2><p>全局能力可在你的所有项目中加载；项目能力只属于当前项目。ZIP、命令、hook 和扩权声明统一预检，风险内容先隔离。</p></div></div>
    <details className="create-disclosure" open={draft ? true : undefined}>
      <summary><Plus size={15} />导入能力</summary>
      <form className="schedule-form capability-import-form" onSubmit={(event) => { event.preventDefault(); if (name.trim()) create.mutate() }}>
        <fieldset className="capability-scope-picker"><legend>安装位置</legend><button type="button" className={scope === 'global' ? 'active' : ''} onClick={() => setScope('global')}><span>全局</span><small>默认 · 所有项目可加载</small></button><button type="button" className={scope === 'project' ? 'active' : ''} onClick={() => setScope('project')}><span>当前项目</span><small>仅此项目可见</small></button></fieldset>
        <button className="capability-file-picker" type="button" onClick={() => manifestFileRef.current?.click()}><Upload size={15} /><span>{sourceFilename || '选择 ZIP / JSON / SKILL.md'}</span><small>ZIP 会校验路径、体积、重复文件和可执行内容</small></button>
        <input ref={manifestFileRef} className="sr-only" type="file" accept="application/zip,.zip,application/json,text/markdown,.json,.md" onChange={async (event) => { const file = event.target.files?.[0]; event.target.value = ''; if (file) await importManifestFile(file) }} />
        {name.trim() ? <div className="capability-import-preview" aria-live="polite"><span>自动识别</span><strong>{name}</strong><small>{kind.toUpperCase()} · v{version || '0.1.0'} · 名称、版本和说明将由内容生成</small></div> : <p className="capability-import-hint">上传后自动识别能力类型，拆解名称、版本、说明和权限声明。</p>}
        <button type="submit" disabled={create.isPending || !name.trim()}>导入并预检</button>
      </form>
    </details>
    {importError || create.error || review.error ? <div className="form-error">{importError || create.error?.message || review.error?.message}</div> : null}
    <div className="capability-scope-filter" aria-label="能力范围筛选">{([['all', '全部'], ['global', '全局'], ['project', '当前项目']] as const).map(([value, label]) => <button type="button" className={scopeFilter === value ? 'active' : ''} onClick={() => setScopeFilter(value)} key={value}>{label}<span>{value === 'all' ? capabilities.length : capabilities.filter((item) => item.scope_type === value).length}</span></button>)}</div>
    <div className="schedule-list">{visibleCapabilities.map((capability) => {
      const health = mcpHealth[capability.id]
      return <article className="schedule-card capability-card" key={capability.id}>
        <div><strong>{capability.name} <em>{capability.kind}</em><span className={`capability-scope-badge ${capability.scope_type}`}>{capability.scope_type === 'global' ? '全局' : '项目'}</span></strong><small>v{capability.version} · {capability.status === 'enabled' ? '已启用' : capability.status === 'quarantined' ? '待审查' : '已停用'} · 风险 {capability.risk_level}</small>{capability.permission_diff.length > 0 ? <small className="capability-permission-diff">请求权限：{capability.permission_diff.join(' · ')}</small> : null}<p>{capability.status === 'quarantined' ? '权限变更尚未获批，当前不会被任务加载。' : health ? `MCP ${health.status} · ${health.tool_count} 个工具${health.last_error ? ` · ${health.last_error}` : ''}` : capability.kind === 'skill' ? '可由你显式使用，也可由模型在任务相关时主动加载。' : '已登记，可在任务配置中选择。'}</p><details className="capability-technical"><summary>技术信息</summary><code>{capability.bundle_hash || capability.content_hash}</code>{capability.source_filename ? <small>来源：{capability.source_filename}</small> : null}<small>安装任务：{capability.install_job_id}</small></details></div>
        <div className="capability-review-actions">{capability.kind === 'skill' && capability.status === 'enabled' ? <button className="primary" type="button" onClick={() => onUseSkill(capability)}><Sparkles size={14} />使用</button> : null}{capability.status === 'quarantined' ? <><button className="primary" type="button" disabled={review.isPending} onClick={() => review.mutate({ id: capability.id, decision: 'approved' })}>审查并启用</button><button type="button" disabled={review.isPending} onClick={() => review.mutate({ id: capability.id, decision: 'rejected' })}>拒绝</button></> : capability.kind === 'mcp' ? <button type="button" disabled={capability.status !== 'enabled' || connect.isPending || disconnect.isPending} onClick={() => health?.status === 'connected' ? disconnect.mutate(capability.id) : connect.mutate(capability.id)}>{health?.status === 'connected' ? '断开 MCP' : '连接 MCP'}</button> : <button type="button" disabled={toggle.isPending} onClick={() => toggle.mutate({ id: capability.id, status: capability.status === 'enabled' ? 'disabled' : 'enabled' })}>{capability.status === 'enabled' ? '停用' : '启用'}</button>}<button type="button" onClick={() => downloadCapabilityManifest(capability)}>导出</button></div>
      </article>
    })}{visibleCapabilities.length === 0 ? <WorkSurfaceEmpty icon={<Puzzle />} title="这个范围还没有能力" copy="导入 ZIP、JSON 或 SKILL.md；未选择位置时默认安装到全局。" /> : null}</div>
  </div>
}

function fileToBase64(file: File) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(new Error('文件读取失败'))
    reader.onload = () => {
      const result = String(reader.result ?? '')
      const separator = result.indexOf(',')
      if (separator < 0) reject(new Error('文件编码失败'))
      else resolve(result.slice(separator + 1))
    }
    reader.readAsDataURL(file)
  })
}

function downloadCapabilityManifest(capability: Awaited<ReturnType<typeof api.capabilities>>['items'][number]) {
  const payload = JSON.stringify({ kind: capability.kind, name: capability.name, version: capability.version, manifest_json: capability.manifest_json, content_hash: capability.content_hash }, null, 2)
  const url = URL.createObjectURL(new Blob([payload], { type: 'application/json' }))
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `${capability.name.replace(/[^a-z0-9._-]+/gi, '-').slice(0, 80) || 'capability'}.manifest.json`
  anchor.click()
  URL.revokeObjectURL(url)
}

function MemoryView({ projectId, memories, query, onQueryChange }: { projectId: string; memories: Awaited<ReturnType<typeof api.memories>>['items']; query: string; onQueryChange: (query: string) => void }) {
  const queryClient = useQueryClient()
  const [title, setTitle] = useState('')
  const [content, setContent] = useState('')
  const [editingID, setEditingID] = useState<string | null>(null)
  const [editTitle, setEditTitle] = useState('')
  const [editContent, setEditContent] = useState('')
  const [editVisibility, setEditVisibility] = useState<Visibility>('private')
  const [editSourceRefs, setEditSourceRefs] = useState('')
  const create = useMutation({ mutationFn: () => api.createMemory(projectId, { kind: 'manual', title, content, visibility: 'private' }), onSuccess: async () => { setTitle(''); setContent(''); await queryClient.invalidateQueries({ queryKey: ['memories', projectId] }) } })
  const daily = useMutation({ mutationFn: () => api.createDailySummary(projectId), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['memories', projectId] }) })
  const promote = useMutation({ mutationFn: (memoryId: string) => api.promoteMemoryToSkill(memoryId), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['capabilities', projectId] }) })
  const update = useMutation({ mutationFn: () => api.updateMemory(editingID!, { title: editTitle, content: editContent, visibility: editVisibility, source_refs: editSourceRefs.split(',').map((ref) => ref.trim()).filter(Boolean) }), onSuccess: async () => { setEditingID(null); await queryClient.invalidateQueries({ queryKey: ['memories', projectId] }) } })
  const toggleStatus = useMutation({ mutationFn: ({ id, status }: { id: string; status: 'active' | 'inactive' }) => api.setMemoryStatus(id, status), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['memories', projectId] }) })
  const remove = useMutation({ mutationFn: (memoryId: string) => api.deleteMemory(memoryId), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['memories', projectId] }) })
  const startEdit = (memory: Memory) => { setEditingID(memory.id); setEditTitle(memory.title); setEditContent(memory.content); setEditVisibility(memory.visibility); setEditSourceRefs((memory.source_refs ?? []).join(', ')) }
  return <div className="memory-surface">
    <div className="automation-intro"><Sparkles size={22} /><div><h2>项目记忆</h2><p>记忆只在相关任务中进入上下文；日报基于已提交事件汇总，不额外调用模型。</p></div><button type="button" onClick={() => daily.mutate()} disabled={daily.isPending}>生成今日日报</button></div>
    <div className="memory-toolbar"><label className="memory-search"><Search size={15} /><span className="sr-only">搜索记忆</span><input aria-label="搜索记忆" value={query} onChange={(event) => onQueryChange(event.target.value)} placeholder="搜索记忆" /></label><details className="create-disclosure"><summary><Plus size={15} />添加记忆</summary><form className="schedule-form" onSubmit={(event) => { event.preventDefault(); if (title.trim() && content.trim()) create.mutate() }}><input aria-label="记忆标题" value={title} onChange={(event) => setTitle(event.target.value)} placeholder="记忆标题" required /><textarea aria-label="记忆内容" value={content} onChange={(event) => setContent(event.target.value)} placeholder="记录经验、决策或可复用知识" required /><button type="submit" disabled={create.isPending || !title.trim() || !content.trim()}>保存记忆</button></form></details></div>
    {create.error || daily.error || promote.error || update.error || toggleStatus.error || remove.error ? <div className="form-error">{create.error?.message ?? daily.error?.message ?? promote.error?.message ?? update.error?.message ?? toggleStatus.error?.message ?? remove.error?.message}</div> : null}
    <div className="memory-list">{memories.map((memory) => <article className="memory-card" key={memory.id}>{editingID === memory.id ? <form className="memory-edit-form" onSubmit={(event) => { event.preventDefault(); if (editTitle.trim() && editContent.trim()) update.mutate() }}><input aria-label={`编辑记忆标题 ${memory.title}`} value={editTitle} onChange={(event) => setEditTitle(event.target.value)} required /><textarea aria-label={`编辑记忆内容 ${memory.title}`} value={editContent} onChange={(event) => setEditContent(event.target.value)} required /><select aria-label={`编辑记忆可见性 ${memory.title}`} value={editVisibility} onChange={(event) => setEditVisibility(event.target.value as Visibility)}><option value="private">仅自己</option><option value="selected">指定成员</option><option value="project">项目成员</option></select><input aria-label={`编辑记忆来源 ${memory.title}`} value={editSourceRefs} onChange={(event) => setEditSourceRefs(event.target.value)} placeholder="来源 ID，逗号分隔（可选）" /><div className="memory-card-actions"><button type="submit" disabled={update.isPending || !editTitle.trim() || !editContent.trim()}>保存修改</button><button type="button" onClick={() => setEditingID(null)}>取消</button></div></form> : <><div className="memory-card-heading"><strong>{memory.title}</strong><div className="memory-card-actions"><button type="button" onClick={() => startEdit(memory)}>编辑</button><button type="button" onClick={() => toggleStatus.mutate({ id: memory.id, status: memory.status === 'active' ? 'inactive' : 'active' })} disabled={toggleStatus.isPending}>{memory.status === 'active' ? '停用' : '启用'}</button><button type="button" onClick={() => remove.mutate(memory.id)} disabled={remove.isPending} aria-label={`删除记忆 ${memory.title}`}>删除</button><button className="primary" type="button" onClick={() => promote.mutate(memory.id)} disabled={promote.isPending || memory.status !== 'active'}>晋级 Skill</button></div></div><small>{memory.kind} · {memory.status === 'active' ? '启用' : '已停用'} · {new Date(memory.created_at).toLocaleString('zh-CN')} · {memory.source_hash.slice(0, 14)}…</small>{memory.source_refs?.length ? <small className="memory-source-refs">来源：{memory.source_refs.join(' · ')}</small> : null}<p>{memory.content}</p></>}</article>)}{memories.length === 0 ? <WorkSurfaceEmpty icon={<Sparkles />} title={query ? '没有匹配的记忆' : '还没有记忆'} copy={query ? '调整搜索词。' : '保存经验，或生成今日零模型调用日报。'} /> : null}</div>
  </div>
}

function AgentView({ projectId, agents, models, modelsError, draft }: { projectId: string; agents: Awaited<ReturnType<typeof api.agents>>['items']; models: { id: string }[]; modelsError?: string; draft?: Extract<ComposerDraft, { kind: 'agent' }> }) {
  const queryClient = useQueryClient()
  const importRef = useRef<HTMLInputElement>(null)
  const [name, setName] = useState(draft?.name ?? '')
  const [version, setVersion] = useState(draft?.version ?? '1.0.0')
  const [instructions, setInstructions] = useState(draft?.instructions ?? '')
  const [model, setModel] = useState(draft?.model ?? '')
  const [importError, setImportError] = useState('')
  const create = useMutation({ mutationFn: () => api.createAgent(projectId, { name, version, instructions, model: model || undefined }), onSuccess: async () => { setName(''); setInstructions(''); await queryClient.invalidateQueries({ queryKey: ['agents', projectId] }) } })
  const toggle = useMutation({ mutationFn: ({ id, status }: { id: string; status: 'enabled' | 'disabled' }) => api.setAgentStatus(id, status), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['agents', projectId] }) })
  return <div className="agent-surface"><div className="automation-intro"><Bot size={22} /><div><h2>版本化 Agent</h2><p>保存指令、模型与配置版本；涉及高风险权限的 Agent 默认进入隔离态，启用前需要项目审批。</p></div><button type="button" className="secondary-button" onClick={() => importRef.current?.click()}>导入 JSON</button><input ref={importRef} className="sr-only" type="file" accept="application/json,.json" onChange={async (event) => { const file = event.target.files?.[0]; event.target.value = ''; if (!file) return; try { const value = JSON.parse(await file.text()) as Record<string, unknown>; const importedName = typeof value.name === 'string' ? value.name.trim() : ''; const importedVersion = typeof value.version === 'string' ? value.version.trim() : ''; const importedInstructions = typeof value.instructions === 'string' ? value.instructions : ''; const importedModel = typeof value.model === 'string' ? value.model.trim() : ''; if (!importedName || !importedVersion || !importedInstructions) throw new Error('JSON 必须包含 name、version、instructions'); setName(importedName); setVersion(importedVersion); setInstructions(importedInstructions); setModel(importedModel); setImportError('') } catch (error) { setImportError(error instanceof Error ? error.message : 'Agent JSON 无法解析') } }} /></div><form className="schedule-form" onSubmit={(event) => { event.preventDefault(); if (name.trim() && instructions.trim()) create.mutate() }}><input aria-label="Agent 名称" value={name} onChange={(event) => setName(event.target.value)} placeholder="Agent 名称" required /><input aria-label="Agent 版本" value={version} onChange={(event) => setVersion(event.target.value)} placeholder="版本，例如 1.0.0" required />{models.length > 0 ? <label className="budget-input"><span>主模型</span><select aria-label="Agent 模型" value={model} onChange={(event) => setModel(event.target.value)}><option value="">项目默认模型</option>{models.map((item) => <option value={item.id} key={item.id}>{item.id}</option>)}</select></label> : <input aria-label="Agent 模型" value={model} onChange={(event) => setModel(event.target.value)} placeholder="主模型（可选；列表不可用时可手填）" />}<textarea aria-label="Agent 指令" value={instructions} onChange={(event) => setInstructions(event.target.value)} placeholder="角色指令与验收要求" required /><button type="submit" disabled={create.isPending || !name.trim() || !instructions.trim()}>保存 Agent</button></form>{modelsError ? <div className="form-hint">模型列表暂不可用：{modelsError}。可手填模型 ID，或先配置模型凭证。</div> : null}{importError ? <div className="form-error">导入失败：{importError}</div> : null}{create.error ? <div className="form-error">{create.error.message}</div> : null}<div className="schedule-list">{agents.map((agent) => <article className="schedule-card" key={agent.id}><div><strong>{agent.name} <em>v{agent.version}</em></strong><small>{agent.model || '项目默认模型'} · 状态 {agent.status}</small><p>{agent.instructions}</p></div><button type="button" disabled={agent.status === 'quarantined' || toggle.isPending} onClick={() => toggle.mutate({ id: agent.id, status: agent.status === 'enabled' ? 'disabled' : 'enabled' })}>{agent.status === 'enabled' ? '停用' : '启用'}</button></article>)}{agents.length === 0 ? <WorkSurfaceEmpty icon={<Bot />} title="还没有自定义 Agent" copy="创建一个有版本、有指令、有模型策略的项目 Agent。" /> : null}</div></div>
}

function TeamDefinitionView({ projectId, teams, draft }: { projectId: string; teams: TeamDefinition[]; draft?: Extract<ComposerDraft, { kind: 'team' }> }) {
  const queryClient = useQueryClient()
  const importRef = useRef<HTMLInputElement>(null)
  const [name, setName] = useState(draft?.name ?? '')
  const [version, setVersion] = useState(draft?.version ?? '1.0.0')
  const [rolesJSON, setRolesJSON] = useState(draft?.rolesJSON ?? '[{"role":"researcher","name":"Researcher"},{"role":"builder","name":"Builder"},{"role":"reviewer","name":"Reviewer"}]')
  const [importError, setImportError] = useState('')
  const create = useMutation({
    mutationFn: () => api.createTeam(projectId, { name, version, roles: JSON.parse(rolesJSON) as { role: string; name: string }[] }),
    onSuccess: async () => { setName(''); await queryClient.invalidateQueries({ queryKey: ['teams', projectId] }) },
  })
  const toggle = useMutation({ mutationFn: ({ id, status }: { id: string; status: CapabilityStatus }) => api.setTeamStatus(id, status), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['teams', projectId] }) })
  return <div className="agent-surface"><div className="automation-intro"><Users size={22} /><div><h2>版本化 Agent Team</h2><p>将 Coordinator 与最多三个 Worker 角色固化为可复用 TeamDefinition；任务启动时固定版本。</p></div><button type="button" className="secondary-button" onClick={() => importRef.current?.click()}>导入 JSON</button><input ref={importRef} className="sr-only" type="file" accept="application/json,.json" onChange={async (event) => { const file = event.target.files?.[0]; event.target.value = ''; if (!file) return; try { const value = JSON.parse(await file.text()) as Record<string, unknown>; const importedName = typeof value.name === 'string' ? value.name.trim() : ''; const importedVersion = typeof value.version === 'string' ? value.version.trim() : ''; const roles = Array.isArray(value.roles) ? value.roles : []; if (!importedName || !importedVersion || roles.length === 0) throw new Error('JSON 必须包含 name、version、roles'); setName(importedName); setVersion(importedVersion); setRolesJSON(JSON.stringify(roles, null, 2)); setImportError('') } catch (error) { setImportError(error instanceof Error ? error.message : 'TeamDefinition JSON 无法解析') } }} /></div><form className="schedule-form" onSubmit={(event) => { event.preventDefault(); if (name.trim()) create.mutate() }}><input aria-label="Team 名称" value={name} onChange={(event) => setName(event.target.value)} placeholder="Team 名称" required /><input aria-label="Team 版本" value={version} onChange={(event) => setVersion(event.target.value)} placeholder="版本，例如 1.0.0" required /><textarea aria-label="Team 角色 JSON" value={rolesJSON} onChange={(event) => setRolesJSON(event.target.value)} /><button type="submit" disabled={create.isPending || !name.trim()}>保存 TeamDefinition</button></form>{importError ? <div className="form-error">导入失败：{importError}</div> : null}{create.error ? <div className="form-error">{create.error.message}</div> : null}<div className="schedule-list">{teams.map((team) => <article className="schedule-card" key={team.id}><div><strong>{team.name} <em>v{team.version}</em></strong><small>{team.roles.map((role) => role.name).join(' · ')} · 状态 {team.status}</small></div><button type="button" disabled={toggle.isPending} onClick={() => toggle.mutate({ id: team.id, status: team.status === 'enabled' ? 'disabled' : 'enabled' })}>{team.status === 'enabled' ? '停用' : '启用'}</button></article>)}{teams.length === 0 ? <WorkSurfaceEmpty icon={<Users />} title="还没有 TeamDefinition" copy="创建一个固定角色、版本和权限边界的 Agent Team。" /> : null}</div></div>
}

function TasksView({ tasks, loading }: { tasks: Awaited<ReturnType<typeof api.tasks>>['items']; loading: boolean }) {
  if (loading) return <div className="task-table-loading">正在读取任务状态…</div>
  if (tasks.length === 0) return <WorkSurfaceEmpty icon={<Bot />} title="没有任务" copy="在下方描述目标，Oktapod 会先建立可恢复的任务记录。" />
  return <div className="task-table"><div className="task-table-head"><span>任务</span><span>模式</span><span>状态</span><span>更新时间</span></div>{tasks.map((task) => <Link className="task-table-row" key={task.id} to="/tasks/$taskId" params={{ taskId: task.id }}><span><strong>{task.title}</strong><small>{task.goal}</small>{task.error_code ? <em className="task-error-code">{task.error_code}</em> : null}</span><span>{task.mode === 'answer' ? '直接回答' : task.mode === 'single_agent' ? '单智能体' : 'Agent Team'}</span><span><em className={`status-pill ${task.status}`}>{statusLabels[task.status]}</em></span><span>{new Date(task.updated_at).toLocaleString('zh-CN')}</span></Link>)}</div>
}

function AssetsView({ projectId }: { projectId: string }) {
  const [folderStack, setFolderStack] = useState<Asset[]>([])
  const [search, setSearch] = useState('')
  const [kind, setKind] = useState<'file' | 'folder' | ''>('')
  const [visibility, setVisibility] = useState<Visibility>('private')
  const [folderName, setFolderName] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)
  const queryClient = useQueryClient()
  const parentId = folderStack.at(-1)?.id
  const assets = useQuery({
    queryKey: ['assets', projectId, parentId ?? '', search, kind],
    queryFn: () => api.assets(projectId, { parentId, query: search.trim(), kind }),
  })
  const createFolder = useMutation({
    mutationFn: () => api.createAssetFolder(projectId, { name: folderName, parent_id: parentId, visibility }),
    onSuccess: async () => {
      setFolderName('')
      await queryClient.invalidateQueries({ queryKey: ['assets', projectId] })
    },
  })
  const upload = useMutation({
    mutationFn: (file: File) => api.uploadAsset(projectId, file, visibility, parentId),
    onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['assets', projectId] }),
  })
  const error = assets.error ?? createFolder.error ?? upload.error
  const capacity = assets.data ? `${formatBytes(assets.data.visible_bytes)} / ${formatBytes(assets.data.quota_bytes)}` : '读取中…'

  return <div className="asset-surface">
    <div className="asset-toolbar">
      <form onSubmit={(event) => { event.preventDefault(); if (folderName.trim()) createFolder.mutate() }}>
        <label><FolderPlus size={16} /><span className="sr-only">新文件夹名称</span><input value={folderName} onChange={(event) => setFolderName(event.target.value)} placeholder="新文件夹名称" /></label>
        <button type="submit" disabled={!folderName.trim() || createFolder.isPending}>新建文件夹</button>
      </form>
      <button type="button" className="asset-upload-button" disabled={upload.isPending} onClick={() => inputRef.current?.click()}><Upload size={16} />{upload.isPending ? '上传中…' : '上传文件'}</button>
      <input ref={inputRef} className="sr-only" data-testid="asset-file-input" type="file" onChange={(event) => { const file = event.target.files?.[0]; if (file) upload.mutate(file); event.target.value = '' }} />
      <label className="asset-visibility"><span>可见性</span><select value={visibility} onChange={(event) => setVisibility(event.target.value as Visibility)}><option value="private">仅自己</option><option value="project">项目成员</option></select></label>
      <span className="asset-capacity"><HardDrive size={15} />{capacity}</span>
    </div>
    <div className="asset-browser-bar">
      <nav aria-label="资产路径"><button type="button" onClick={() => setFolderStack([])}>资产</button>{folderStack.map((folder, index) => <span key={folder.id}><ChevronRight size={13} /><button type="button" onClick={() => setFolderStack((current) => current.slice(0, index + 1))}>{folder.name}</button></span>)}</nav>
      <div className="asset-filters"><label><Search size={15} /><span className="sr-only">搜索资产</span><input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索文件或文件夹" /></label><select aria-label="资产类型" value={kind} onChange={(event) => setKind(event.target.value as typeof kind)}><option value="">全部类型</option><option value="folder">文件夹</option><option value="file">文件</option></select></div>
    </div>
    {error ? <div className="asset-error">{error.message}</div> : null}
    <div className="asset-table" aria-label="项目资产">
      <div className="asset-table-head"><span>名称</span><span>类型 / 来源</span><span>更新人</span><span>更新时间</span><span>大小</span><span /></div>
      {assets.isLoading ? <div className="asset-empty">正在渐进加载元数据…</div> : null}
      {assets.data?.items.map((asset) => <div className="asset-row" key={asset.id}>
        <span className="asset-name">{asset.kind === 'folder' ? <Folder size={19} /> : <FileIcon size={19} />}{asset.kind === 'folder' ? <button type="button" onClick={() => setFolderStack((current) => [...current, asset])}>{asset.name}</button> : <a href={api.assetPreviewUrl(asset.id)} target="_blank" rel="noreferrer">{asset.name}</a>}<em>{asset.visibility === 'private' ? '仅自己' : '项目'}</em></span>
        <span>{asset.kind === 'folder' ? '文件夹' : asset.media_type || '文件'}<small>{asset.source_type === 'artifact' ? '任务产物' : '上传'}</small></span>
        <span title={asset.owner_user_id}>{asset.owner_user_id.slice(0, 8)}</span>
        <span>{new Date(asset.updated_at).toLocaleString('zh-CN')}</span>
        <span>{asset.kind === 'folder' ? '—' : formatBytes(asset.size_bytes)}</span>
        <span>{asset.kind === 'file' ? <a aria-label={`下载 ${asset.name}`} href={api.assetContentUrl(asset.id)}><Download size={16} /></a> : <button aria-label={`打开 ${asset.name}`} type="button" onClick={() => setFolderStack((current) => [...current, asset])}><ChevronRight size={16} /></button>}</span>
      </div>)}
      {!assets.isLoading && assets.data?.items.length === 0 ? <div className="asset-empty"><FileBox size={26} /><strong>{search ? '没有匹配的资产' : '当前目录为空'}</strong><small>{search ? '调整搜索词或类型筛选。' : '上传文件，或从任务结果发布可复用资产。'}</small></div> : null}
    </div>
  </div>
}

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KiB`
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MiB`
  return `${(value / 1024 ** 3).toFixed(2)} GiB`
}

function WorkSurfaceEmpty({ icon, title, copy }: { icon: React.ReactNode; title: string; copy: string }) {
  return <div className="surface-empty"><span>{icon}</span><h2>{title}</h2><p>{copy}</p></div>
}

function parseComposerDraft(text: string): ComposerDraft | null {
  const normalized = text.trim()
  const scheduleMatch = normalized.match(/^创建计划\s*[:：]\s*(.+)$/i)
  if (scheduleMatch) {
    const parts = (scheduleMatch[1] ?? '').split('|').map((part) => part.trim())
    const [name, cron, timezone, goal] = parts
    if (parts.length === 4 && name && cron && timezone && goal) {
      return { kind: 'schedule', name, cron, timezone, goal }
    }
  }
  const teamMatch = normalized.match(/^创建\s*(Agent\s*Team|TeamDefinition|Team)\s*[:：]\s*(.+)$/i)
  if (teamMatch) {
    const parts = (teamMatch[2] ?? '').split('|').map((part) => part.trim())
    const [name, version, rolesJSON] = parts
    if (parts.length === 3 && name && version && rolesJSON) {
      try {
        const roles = JSON.parse(rolesJSON) as unknown
        if (Array.isArray(roles) && roles.length > 0) {
          return { kind: 'team', name, version, rolesJSON: JSON.stringify(roles, null, 2) }
        }
      } catch {
        return null
      }
    }
  }
  const agentMatch = normalized.match(/^创建\s*Agent\s*[:：]\s*(.+)$/i)
  if (agentMatch) {
    const parts = (agentMatch[1] ?? '').split('|').map((part) => part.trim())
    const [name, version, instructions, model = ''] = parts
    if ((parts.length === 3 || parts.length === 4) && name && version && instructions) {
      return { kind: 'agent', name, version, instructions, model }
    }
  }
  const skillMatch = normalized.match(/^创建\s*(Skill|插件|MCP)\s*[:：]\s*(.+)$/i)
  if (skillMatch) {
    const parts = (skillMatch[2] ?? '').split('|').map((part) => part.trim())
    const [name, version, instructions] = parts
    if (parts.length === 3 && name && version && instructions) {
      const kindMap: Record<string, CapabilityKind> = { skill: 'skill', 插件: 'plugin', mcp: 'mcp', agent: 'agent' }
      const capabilityKind = kindMap[(skillMatch[1] ?? '').toLowerCase()] ?? 'skill'
      return { kind: 'capability', capabilityKind, name, version, manifest: JSON.stringify({ description: instructions, instructions }, null, 2) }
    }
  }
  return null
}

function skillDescription(skill: Capability) {
  try {
    const manifest = JSON.parse(skill.manifest_json) as { description?: unknown }
    return typeof manifest.description === 'string' ? manifest.description.trim() : ''
  } catch {
    return ''
  }
}

function TaskComposer({ projectId, conversationId, activeTask, agents, skills, providers, initialValue, onComposerDraft, onConversationSelected, onOptimisticMessage, onRoute, onDelta, onFailed, onSettled }: { projectId: string; conversationId?: string; activeTask?: Awaited<ReturnType<typeof api.tasks>>['items'][number]; agents: Awaited<ReturnType<typeof api.agents>>['items']; skills: Capability[]; providers: ProviderInstance[]; initialValue?: string; onComposerDraft: (draft: ComposerDraft) => void; onConversationSelected: (id: string) => void; onOptimisticMessage: (content: string) => void; onRoute: (route: 'chat' | 'task') => void; onDelta: (delta: string) => void; onFailed: () => void; onSettled: () => void }) {
  const bootstrap = useBootstrap()
  const storageKey = `oktapod.composer.${bootstrap.user.id}.${projectId}.${conversationId ?? 'new'}`
  const permissionStorageKey = `oktapod.permission-mode.${bootstrap.user.id}.${projectId}`
  const treePrefillKey = conversationId ? `oktapod.composer.prefill.${conversationId}` : ''
  const [goal, setGoal] = useState(() => initialValue ?? (treePrefillKey ? window.sessionStorage.getItem(treePrefillKey) : undefined) ?? window.localStorage.getItem(storageKey) ?? '')
  const [mode, setMode] = useState<TaskMode>(initialValue ? 'single_agent' : 'auto')
  const [permissionMode, setPermissionMode] = useState<PermissionMode>(() => {
    const saved = window.localStorage.getItem(permissionStorageKey)
    return saved === 'ask' || saved === 'auto_review' || saved === 'full_access' ? saved : 'ask'
  })
  const [fullAccessOpen, setFullAccessOpen] = useState(false)
  const [scene, setScene] = useState<Scene>('work')
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [agentId, setAgentId] = useState('default')
  const [providerId, setProviderId] = useState('default')
  const [thinking, setThinking] = useState<'auto' | 'off' | 'low' | 'medium' | 'high'>('auto')
  const [skillCommandError, setSkillCommandError] = useState('')
  const [capabilityInstallMessage, setCapabilityInstallMessage] = useState('')
  const [selectedSkillIndex, setSelectedSkillIndex] = useState(0)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const capabilityAttachmentRef = useRef<HTMLInputElement>(null)
  const queryClient = useQueryClient()
  const submit = useMutation({
    mutationFn: ({ content, invocation, queueKind }: { content: string; invocation: ReturnType<typeof parseSkillInvocation>; queueKind: 'follow_up' | 'steering' }) => api.streamProjectTurn(projectId, { conversation_id: conversationId, content, mode: isCompactCommand(content) ? 'answer' : invocation ? 'single_agent' : mode, visibility: 'private', permission_mode: permissionMode, queue_kind: queueKind, scene, provider_id: providerId === 'default' ? undefined : providerId, thinking, agent_id: agentId === 'default' ? undefined : agentId, skill_id: invocation?.skillId, skill_args: invocation?.skillArgs }, { onRoute, onDelta }),
    onSuccess: async (result) => {
      onConversationSelected(result.conversation.id)
      await queryClient.invalidateQueries({ queryKey: ['tasks', projectId] })
      await queryClient.invalidateQueries({ queryKey: ['project-activity', projectId] })
      await queryClient.invalidateQueries({ queryKey: ['conversations', projectId] })
      await queryClient.invalidateQueries({ queryKey: ['conversation-messages', result.conversation.id] })
      await queryClient.invalidateQueries({ queryKey: ['plan-items', projectId] })
      onSettled()
    },
    onError: () => onFailed(),
  })
  const abort = useMutation({ mutationFn: () => api.controlTask(activeTask!.id, 'cancel'), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['tasks', projectId] }) })
  const installCapability = useMutation({
    mutationFn: async (file: File) => {
      if (!/\.zip$/i.test(file.name)) throw new Error('对话附件安装当前支持 ZIP 包；SKILL.md 和 JSON 可在能力中心导入。')
      return api.createCapability(projectId, { kind: 'skill', name: file.name.replace(/\.zip$/i, ''), version: '0.1.0', manifest_json: '{}', scope_type: 'global', source_filename: file.name, bundle_base64: await fileToBase64(file) })
    },
    onSuccess: async (capability) => {
      setCapabilityInstallMessage(`${capability.name} 已${capability.status === 'quarantined' ? '进入隔离审查' : '安装到全局'}`)
      await queryClient.invalidateQueries({ queryKey: ['capabilities', projectId] })
    },
  })

  useEffect(() => {
    if (!initialValue) return
    window.requestAnimationFrame(() => textareaRef.current?.focus())
  }, [initialValue])
  useEffect(() => {
    const timer = window.setTimeout(() => {
      if (!goal.trim() || containsDraftSecret(goal)) window.localStorage.removeItem(storageKey)
      else window.localStorage.setItem(storageKey, goal)
    }, 200)
    return () => window.clearTimeout(timer)
  }, [goal, storageKey])

  const slashMatch = goal.match(/^\/([^\s]*)$/)
  const slashQuery = (slashMatch?.[1] ?? '').toLowerCase()
  const commandSuggestions = slashMatch ? [
    ...('compact'.includes(slashQuery) ? [{ kind: 'system' as const, command: 'compact', description: '压缩当前会话上下文，原始消息仍保留' }] : []),
    ...skills.filter((skill) => skillCommandName(skill.name).includes(slashQuery)).slice(0, 8).map((skill) => ({ kind: 'skill' as const, skill })),
  ] : []

  const activeSkillIndex = Math.min(selectedSkillIndex, Math.max(0, commandSuggestions.length - 1))

  const chooseCommand = (suggestion: (typeof commandSuggestions)[number]) => {
    if (suggestion.kind === 'system') {
      setGoal('/compact ')
      setMode('answer')
    } else {
      setGoal(`/${skillCommandName(suggestion.skill.name)} `)
      setMode('single_agent')
    }
    setSkillCommandError('')
    window.requestAnimationFrame(() => textareaRef.current?.focus())
  }

  const send = (queueKind: 'follow_up' | 'steering' = activeTask ? 'steering' : 'follow_up') => {
    if (!goal.trim() || submit.isPending) return
    const content = goal.trim()
    const draft = parseComposerDraft(content)
    if (draft) { onComposerDraft(draft); setGoal(''); return }
    const invocation = parseSkillInvocation(content, skills)
    if (content.startsWith('/') && !invocation && !isCompactCommand(content)) {
      setSkillCommandError('没有找到可用命令，请从列表选择。')
      return
    }
    setGoal('')
    window.localStorage.removeItem(storageKey)
    if (treePrefillKey) window.sessionStorage.removeItem(treePrefillKey)
    setSkillCommandError('')
    onOptimisticMessage(content)
    submit.mutate({ content, invocation, queueKind })
  }

  const enabledAgents = agents.filter((agent) => agent.status === 'enabled')
  const executorItems: [string, string][] = enabledAgents.length > 0
	? [['default', '内置执行器'], ...enabledAgents.map((agent) => [agent.id, agent.name] as [string, string])]
	: [['default', '内置执行器']]
  const providerItems: [string, string][] = [
    ['default', '默认 Provider'],
    ...providers.filter((provider) => provider.status === 'active').map((provider) => [provider.provider_id, provider.display_name] as [string, string]),
  ]
  const selectPermissionMode = (value: PermissionMode) => {
    setPermissionMode(value)
    window.localStorage.setItem(permissionStorageKey, value)
  }

  return <div className="conversation-composer-wrap">
    {skillCommandError || submit.error || installCapability.error || abort.error ? <div className="composer-error" role="alert">{skillCommandError || submit.error?.message || installCapability.error?.message || abort.error?.message}</div> : null}
    {capabilityInstallMessage ? <div className="composer-install-status"><Check size={14} />{capabilityInstallMessage}</div> : null}
    {commandSuggestions.length > 0 ? <div className="skill-command-menu" role="listbox" aria-label="Skills">
      <div className="skill-command-menu-heading"><Sparkles size={14} /><span>命令</span><small>{commandSuggestions.length}</small></div>
      {commandSuggestions.map((suggestion, index) => suggestion.kind === 'system'
        ? <button type="button" role="option" aria-selected={activeSkillIndex === index} className={activeSkillIndex === index ? 'active' : ''} key="system-compact" onMouseDown={(event) => event.preventDefault()} onClick={() => chooseCommand(suggestion)}><span className="skill-command-icon"><Workflow size={15} /></span><span><strong>/compact</strong><small>{suggestion.description}</small></span><em>系统</em></button>
        : <button type="button" role="option" aria-selected={activeSkillIndex === index} className={activeSkillIndex === index ? 'active' : ''} key={suggestion.skill.id} onMouseDown={(event) => event.preventDefault()} onClick={() => chooseCommand(suggestion)}><span className="skill-command-icon"><Puzzle size={15} /></span><span><strong>/{skillCommandName(suggestion.skill.name)}</strong><small>{skillDescription(suggestion.skill) || `Skill v${suggestion.skill.version}`}</small></span><em>v{suggestion.skill.version}</em></button>)}
    </div> : null}
    <form className="conversation-composer" noValidate onSubmit={(event) => { event.preventDefault(); send() }}>
      <textarea ref={textareaRef} aria-label="消息" value={goal} onChange={(event) => { setGoal(event.target.value); setSelectedSkillIndex(0); setSkillCommandError('') }} onKeyDown={(event) => {
        if (commandSuggestions.length > 0 && event.key === 'ArrowDown') { event.preventDefault(); setSelectedSkillIndex((current) => (current + 1) % commandSuggestions.length); return }
        if (commandSuggestions.length > 0 && event.key === 'ArrowUp') { event.preventDefault(); setSelectedSkillIndex((current) => (current - 1 + commandSuggestions.length) % commandSuggestions.length); return }
        if (commandSuggestions.length > 0 && event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); const selectedCommand = commandSuggestions[activeSkillIndex] ?? commandSuggestions[0]; if (selectedCommand) chooseCommand(selectedCommand); return }
        if (event.key === 'Escape' && activeTask && !abort.isPending) { event.preventDefault(); abort.mutate(); return }
        if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); send(event.altKey ? 'follow_up' : activeTask ? 'steering' : 'follow_up') }
      }} placeholder="发消息，或描述要完成的任务" rows={2} />
      {advancedOpen ? <div className="composer-advanced">
        <div className="composer-advanced-row"><InlineSelect value={scene} onValueChange={(value) => setScene(value as Scene)} items={[["work", "通用场景"], ["code", "代码场景"]]} /><InlineSelect value={providerId} onValueChange={setProviderId} items={providerItems} /><InlineSelect value={thinking} onValueChange={(value) => setThinking(value as typeof thinking)} items={[["auto", "Thinking · Auto"], ["off", "Thinking · Off"], ["low", "Thinking · Low"], ["medium", "Thinking · Medium"], ["high", "Thinking · High"]]} />{mode !== 'answer' ? <InlineSelect value={agentId} onValueChange={setAgentId} items={executorItems} /> : null}</div>
        <p className="composer-runtime-defaults">运行默认值：模型交互最多重试 {TASK_RUNTIME_DEFAULTS.modelRetryLimit} 次、失败工具恢复最多 {TASK_RUNTIME_DEFAULTS.toolRetryLimit} 次；单次输出按 {TASK_RUNTIME_DEFAULTS.modelOutputTokens[0].toLocaleString('zh-CN')} → {TASK_RUNTIME_DEFAULTS.modelOutputTokens[1].toLocaleString('zh-CN')} → {TASK_RUNTIME_DEFAULTS.modelOutputTokens[2].toLocaleString('zh-CN')} Token 递增；上下文默认保留最近 {TASK_RUNTIME_DEFAULTS.contextRetainTokens.toLocaleString('zh-CN')} Token。</p>
      </div> : null}
      <div className="composer-toolbar"><div><button className="composer-icon" type="button" aria-label="安装能力附件" title="安装 ZIP 能力包（默认全局）" disabled={installCapability.isPending} onClick={() => { setCapabilityInstallMessage(''); capabilityAttachmentRef.current?.click() }}><Paperclip size={16} /></button><input ref={capabilityAttachmentRef} className="sr-only" type="file" accept="application/zip,.zip" onChange={(event) => { const file = event.target.files?.[0]; event.target.value = ''; if (file) installCapability.mutate(file) }} /><InlineSelect value={mode} onValueChange={(value) => setMode(value as TaskMode)} items={[["auto", "Auto"], ["answer", "Ask"], ["single_agent", "Task"], ["agent_team", "Agent Team"]]} /><InlineSelect value={permissionMode} onValueChange={(value) => { if (value === 'full_access') { setFullAccessOpen(true); return } selectPermissionMode(value as PermissionMode) }} items={[["ask", "请我审批"], ["auto_review", "自动审查"], ["full_access", "Full Access"]]} /><span className="composer-execution-boundary" title="命令在当前宿主机受策略和凭据隔离控制，但不具备容器或操作系统级强隔离">宿主受控 · 非强沙箱</span><button className={`composer-icon ${advancedOpen ? 'active' : ''}`} type="button" aria-label="执行设置" title="场景与执行器" onClick={() => setAdvancedOpen((value) => !value)}><SlidersHorizontal size={16} /></button></div><div className="composer-send-group">{activeTask ? <button className="composer-next-turn" disabled={submit.isPending || !goal.trim()} type="button" aria-label="加入 Next Turn" title="加入 Next Turn" onClick={() => send('follow_up')}>Next Turn</button> : null}<button className="composer-send" disabled={submit.isPending || !goal.trim()} type="submit" aria-label={activeTask ? '发送 Steering' : '发送'} title={activeTask ? '发送 Steering' : '发送'}><ArrowUp size={18} /></button></div></div>
    </form>
    <Dialog.Root open={fullAccessOpen} onOpenChange={setFullAccessOpen}>
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content className="dialog-content permission-mode-dialog" aria-describedby="full-access-description">
          <Dialog.Title>启用 Full Access？</Dialog.Title>
          <Dialog.Description id="full-access-description">它会用于后续发送的任务，直到你在发送框中手动切换权限模式。它不会绕过路径、网络、命令黑名单或场景能力边界；破坏性和开放世界 MCP 仍需人工确认。</Dialog.Description>
          <div className="dialog-actions"><Dialog.Close asChild><button type="button">取消</button></Dialog.Close><button className="primary" type="button" onClick={() => { selectPermissionMode('full_access'); setFullAccessOpen(false) }}>确认启用</button></div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  </div>
}

function containsDraftSecret(value: string) {
  return /(?:api[_-]?key|authorization|bearer|password|secret)\s*[:=]\s*\S{8,}/i.test(value)
    || /\bsk-[a-z0-9_-]{16,}\b/i.test(value)
}

function InlineSelect({ value, onValueChange, items }: { value: string; onValueChange: (value: string) => void; items: [string, string][] }) {
  return <Select.Root value={value} onValueChange={onValueChange}><Select.Trigger className="inline-select"><Select.Value /><Select.Icon><ChevronDown size={14} /></Select.Icon></Select.Trigger><Select.Portal><Select.Content className="select-content" position="popper"><Select.Viewport>{items.map(([itemValue, label]) => <Select.Item className="select-item" value={itemValue} key={itemValue}><Select.ItemText>{label}</Select.ItemText><Select.ItemIndicator><Check size={14} /></Select.ItemIndicator></Select.Item>)}</Select.Viewport></Select.Content></Select.Portal></Select.Root>
}
