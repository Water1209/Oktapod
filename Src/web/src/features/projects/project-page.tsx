import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import * as Select from '@radix-ui/react-select'
import * as Dialog from '@radix-ui/react-dialog'
import { ArrowUp, Bot, Check, ChevronDown, ChevronRight, Clock3, Download, File as FileIcon, FileBox, FileCheck2, Folder, FolderInput, FolderPlus, HardDrive, Infinity as InfinityIcon, LockKeyhole, Maximize2, MessageSquare, Minimize2, MoreHorizontal, PanelRight, PanelRightClose, Paperclip, Plus, Puzzle, RefreshCw, Search, Settings2, ShieldAlert, SlidersHorizontal, Sparkles, Square, Upload, Users, Workflow, X, XCircle } from 'lucide-react'
import { api, type Artifact, type Asset, type Capability, type CapabilityKind, type CapabilityScope, type CapabilityStatus, type ConversationMessage, type MCPHealth, type Memory, type PermissionMode, type PlanItem, type PlanStatus, type ProjectActivity, type ProjectInvitation, type ProviderInstance, type ResourceInstallDraft, type Scene, type TaskMode, type TeamDefinition, type Visibility, type WorkspaceFile } from '../../shared/api/client'
import { AssistantContent } from '../../shared/assistant-content'
import { useBootstrap } from '../../shared/bootstrap-context'
import { TASK_RUNTIME_DEFAULTS } from '../../shared/runtime-defaults'
import brandMark from '../../shared/brand-mark.png'
import { V1_VISIBILITY, visibleInspectorTab } from '../../shared/v1-visibility'
import { FileReviewProvider } from '../tasks/multi-file-review'
import { useFileReview } from '../tasks/file-review-context'
import { MessageTaskStream } from '../tasks/message-task-stream'
import { latestDeliverableArtifacts } from '../tasks/artifact-versions'
import { TaskStatusStrip } from '../tasks/task-status-strip'
import { useRunEvents } from '../runs/use-run-events'
import { isCompactCommand, parseSkillInvocation, skillCommandName } from './skill-command'
import { InspectorChanges, InspectorContext, InspectorEvents, InspectorRun, InspectorSecurity, InspectorUsage } from './project-inspector'
import { ResourceInstallDialog } from '../resources/resources-page'

const statusLabels: Record<string, string> = {
  queued: '待开始', running: '运行中', waiting_approval: '等待审批', waiting_input: '等待输入', paused: '已暂停', blocked: '执行受阻', succeeded: '已完成', failed: '失败', canceled: '已取消',
}

type ComposerDraft =
  | { kind: 'schedule'; name: string; cron: string; timezone: string; goal: string }
  | { kind: 'capability'; capabilityKind: CapabilityKind; name: string; version: string; manifest: string }
  | { kind: 'agent'; name: string; version: string; instructions: string; model: string }
  | { kind: 'team'; name: string; version: string; rolesJSON: string }

type ComposerAttachment = {
  key: string
  file?: File
  asset?: Asset
  name: string
  size: number
  source: 'upload' | 'project'
  status: 'uploading' | 'ready' | 'failed'
  progress: number
  error?: string
}

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
  const capabilities = useQuery({ queryKey: ['capabilities', projectId], queryFn: () => api.capabilities(projectId) })
  const agents = useQuery({ queryKey: ['agents', projectId], queryFn: () => api.agents(projectId) })
  const models = useQuery({ queryKey: ['models'], queryFn: () => api.models(), retry: false, staleTime: 60_000 })
  const providers = useQuery({ queryKey: ['providers'], queryFn: api.providers, retry: false, staleTime: 60_000 })
  const diagnostics = useQuery({ queryKey: ['diagnostics'], queryFn: api.diagnostics, retry: false, staleTime: 30_000 })
  const [membersOpen, setMembersOpen] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const inspectorStorageKey = `oktapod.inspector.${bootstrap.user.id}.${projectId}`
  const [managementOpen, setManagementOpen] = useState(Boolean(inspectorSearch))
  const [managementTab, setManagementTab] = useState(() => visibleInspectorTab(inspectorSearch || window.localStorage.getItem(inspectorStorageKey) || undefined))
  const [managementMoreOpen, setManagementMoreOpen] = useState(false)
  const [managementMaximized, setManagementMaximized] = useState(false)
  const [filesSection, setFilesSection] = useState<'workspace' | 'project' | 'outputs'>('workspace')
  const activeConversationId = conversationSearch === 'new' ? undefined : threadId ?? conversationSearch ?? conversations.data?.items[0]?.id
  const messages = useQuery({ queryKey: ['conversation-messages', activeConversationId], queryFn: () => api.conversationMessages(activeConversationId ?? ''), enabled: Boolean(activeConversationId), refetchInterval: 1_500 })
  const usage = useQuery({ queryKey: ['project-usage', projectId], queryFn: () => api.projectUsage(projectId), enabled: managementOpen && managementTab === 'project_usage', staleTime: 15_000 })
  const [composerDraft, setComposerDraft] = useState<ComposerDraft | undefined>()
  const [resourceInstallDraft, setResourceInstallDraft] = useState<ResourceInstallDraft>()
  const [resourceInstallConversationId, setResourceInstallConversationId] = useState('')
  const [resourceInstallNotice, setResourceInstallNotice] = useState('')
  const [resourceInstallError, setResourceInstallError] = useState('')
  const [pendingMessages, setPendingMessages] = useState<Awaited<ReturnType<typeof api.conversationMessages>>['items']>([])
  const [streamingAssistant, setStreamingAssistant] = useState('')
  const [streamPhase, setStreamPhase] = useState<'routing' | 'responding' | 'task' | undefined>()
  const threadRef = useRef<HTMLDivElement>(null)

  const handleComposerDraft = (draft: ComposerDraft) => {
    setComposerDraft(draft)
    setManagementOpen(true)
    setManagementTab(draft.kind === 'capability' ? 'tools' : 'agents')
    setManagementMoreOpen(true)
  }

  const selectedConversation = conversations.data?.items.find((item) => item.id === activeConversationId)
  const conversationTasks = (tasks.data?.items ?? []).filter((task) => task.conversation_id === activeConversationId)
  const activeConversationTasks = conversationTasks.filter((task) => !['succeeded', 'failed', 'blocked', 'canceled'].includes(task.status))
  const allConversationTasks = [...activeConversationTasks, ...conversationTasks.filter((task) => ['succeeded', 'failed', 'blocked', 'canceled'].includes(task.status))]
  const inspectedTask = allConversationTasks[0]
  const inspectedRunEvents = useRunEvents(inspectedTask?.run_id, Boolean(inspectedTask && managementOpen))
  const inspectedUsage = useQuery({ queryKey: ['run-usage', inspectedTask?.run_id], queryFn: () => api.runUsage(inspectedTask!.run_id), enabled: Boolean(inspectedTask?.run_id && managementOpen && ['run', 'usage'].includes(managementTab)) })
  const inspectedArtifacts = useQuery({ queryKey: ['run-artifacts', inspectedTask?.run_id], queryFn: () => api.runArtifacts(inspectedTask!.run_id), enabled: Boolean(inspectedTask?.run_id && managementOpen && ['run', 'changes', 'files'].includes(managementTab)) })
  const inspectedArtifactVersions = latestDeliverableArtifacts(inspectedArtifacts.data?.items ?? [])
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
    tab = visibleInspectorTab(tab)
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
    <div className={`project-chat-workspace ${managementOpen ? 'drawer-open' : ''} ${managementMaximized ? 'drawer-maximized' : ''}`}>
      <main className="conversation-main">
        <header className="conversation-header">
          <div><strong>{selectedConversation?.title || '新会话'}</strong><span><LockKeyhole size={12} />个人私有</span></div>
          <div className="conversation-header-actions">
            <button className="icon-control" type="button" aria-label="新建会话" title="新建会话" onClick={() => void navigate({ to: '/projects/$projectId', params: { projectId }, search: { conversation: 'new' } })}><Plus size={17} /></button>
            {V1_VISIBILITY.teamProjects && project.data.kind === 'team' ? <button className="icon-control" type="button" aria-label="项目成员" title="项目成员" onClick={() => setMembersOpen(true)}><Users size={17} /></button> : null}
            <button className="icon-control" type="button" aria-label={managementOpen ? '隐藏任务面板' : '打开任务面板'} title={managementOpen ? '隐藏任务面板' : '打开任务面板'} onClick={() => { if (managementOpen) { setManagementOpen(false); setManagementMaximized(false); updateInspectorLocation() } else selectInspectorTab(managementTab) }}>{managementOpen ? <PanelRightClose size={18} /> : <PanelRight size={18} />}</button>
          </div>
        </header>

        <div className="message-thread" aria-live="polite" ref={threadRef}>
          {messages.isLoading ? <div className="thread-state">正在载入会话…</div> : null}
          {!messages.isLoading && visibleMessages.length === 0 && !(streamVisible && streamPhase) ? <div className="thread-welcome"><span className="thread-welcome-mark"><img src={brandMark} alt="Oktapod" /></span><h1>{project.data.name}</h1><p>{project.data.description || '在这里提问、讨论，或直接交给 Oktapod 执行。自动模式会判断是否需要创建任务。'}</p></div> : null}
          {visibleMessages.map((message, messageIndex) => {
            const linkedTask = message.task_id ? conversationTasks.find((task) => task.id === message.task_id) : undefined
            const taskIsActive = Boolean(linkedTask && ['queued', 'running', 'waiting_approval', 'waiting_input'].includes(linkedTask.status))
            const displayedContent = legacySafeMessageContent(message, linkedTask?.status)
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
                  <div className="message-body">{message.role === 'assistant' ? <AssistantContent content={displayedContent} /> : displayedContent}</div>
                  {message.role === 'user' ? <MessagePresentation presentation={message.presentation} /> : null}
                </article>
                <div className="thread-task-stream">
                  <MessageTaskStream taskId={message.task_id!} runId={linkedTask!.run_id ?? ''} active={taskIsActive} onRequestFileReview={() => { setFilesSection('outputs'); selectInspectorTab('files') }} />
                </div>
              </div>
            }
            return <article key={message.id} className={`thread-message ${message.role}${conversationCommand ? ' conversation-command' : ''}`}>
  <div className="message-role">{roleLabel}</div>
  <div className="message-body">
    {message.role === 'assistant' ? <AssistantContent content={displayedContent} /> : displayedContent}
  </div>
  {message.role === 'user' ? <MessagePresentation presentation={message.presentation} /> : null}
</article>
          })}
          {streamVisible && streamPhase ? <article className="thread-message assistant streaming"><div className="message-role">Oktapod</div><div className="message-body">{streamingAssistant ? <><AssistantContent content={streamingAssistant} streaming /><span className="stream-caret" aria-hidden="true" /></> : <>{streamPhase === 'routing' ? '正在判断如何处理…' : streamPhase === 'task' ? '正在创建任务…' : '正在生成回复…'}<span className="stream-caret" aria-hidden="true" /></>}</div></article> : null}
        </div>

        <div className="composer-stack">
          <TaskStatusStrip activeTask={allConversationTasks[0]} conversationId={activeConversationId} />
          {resourceInstallNotice ? <div className="composer-install-status" role="status"><Check size={14} />{resourceInstallNotice}</div> : null}
          {resourceInstallError ? <p className="form-error composer-resource-error" role="alert">{resourceInstallError}</p> : null}
          <TaskComposer
            key={`composer-${activeConversationKey}`}
            projectId={projectId}
            conversationId={activeConversationId}
            activeTask={activeConversationTasks[0]}
            agents={agents.data?.items ?? []}
            skills={(capabilities.data?.items ?? []).filter((item) => item.kind === 'skill' && item.status === 'enabled')}
            providers={providers.data?.items ?? []}
            models={models.data?.items ?? []}
            projectModelProviderId={project.data.model_provider_id}
            projectDefaultModel={project.data.default_model}
            projectScene={project.data.scene}
            autoReviewAvailable={diagnostics.data?.auto_review.available}
            autoReviewReason={diagnostics.data?.auto_review.reason}
            onComposerDraft={handleComposerDraft}
            onResourceInstallDraft={(draft, conversationId) => { setResourceInstallDraft(draft); setResourceInstallConversationId(conversationId); setResourceInstallError(''); setResourceInstallNotice('') }}
            onConversationSelected={selectConversation}
            onOptimisticMessage={(clientMessageId, content, presentation) => {
              setPendingMessages((current) => [...current, {
                id: clientMessageId,
                project_id: projectId,
                conversation_id: activeConversationKey,
                task_id: null,
                server_seq: Number.MAX_SAFE_INTEGER,
                role: 'user',
                visibility: 'private',
                content,
                presentation,
                created_at: new Date().toISOString(),
              }])
              if (!activeConversationTasks[0]) {
                setStreamingAssistant('')
                setStreamPhase('routing')
              }
            }}
            onRoute={(route) => setStreamPhase(route === 'task' ? 'task' : 'responding')}
            onDelta={(delta) => { setStreamPhase('responding'); setStreamingAssistant((current) => current + delta) }}
            onFailed={(clientMessageId) => {
              setPendingMessages((current) => current.filter((message) => message.id !== clientMessageId))
              if (!activeConversationTasks[0]) {
                setStreamingAssistant('')
                setStreamPhase(undefined)
              }
            }}
            onSettled={(clientMessageId) => {
              setPendingMessages((current) => current.filter((message) => message.id !== clientMessageId))
              if (!activeConversationTasks[0]) {
                setStreamingAssistant('')
                setStreamPhase(undefined)
              }
            }}
          />
        </div>
      </main>

      {resourceInstallDraft ? <ResourceInstallDialog projectId={projectId} initialDraft={resourceInstallDraft} onClose={() => { setResourceInstallDraft(undefined); if (resourceInstallConversationId) selectConversation(resourceInstallConversationId) }} onInstalled={async (capability) => {
        try {
          if (capability.status === 'quarantined') await api.reviewCapability(capability.id, 'approved', '用户通过对话明确请求安装并在安全窗口核对 MCP 来源、权限与凭据配置')
          const health = await api.connectMCP(capability.id)
          await queryClient.invalidateQueries({ queryKey: ['capabilities', projectId] })
          setResourceInstallNotice(`${capability.name} 已安全安装并连接，发现 ${health.tool_count} 个 MCP 工具。`)
          setResourceInstallDraft(undefined)
          if (resourceInstallConversationId) selectConversation(resourceInstallConversationId)
        } catch (error) {
          setResourceInstallError(error instanceof Error ? error.message : 'MCP 安装后连接失败。')
          throw error
        }
      }} /> : null}

      {managementOpen ? <aside className="project-management-panel">
        <header><div><strong>任务面板</strong><span>{inspectedTask?.title || project.data.name}</span></div><span className="panel-header-actions"><button className="icon-control" type="button" aria-label={managementMaximized ? '还原任务面板' : '最大化任务面板'} title={managementMaximized ? '还原任务面板' : '最大化任务面板'} onClick={() => setManagementMaximized((value) => !value)}>{managementMaximized ? <Minimize2 size={16} /> : <Maximize2 size={16} />}</button><button className="icon-control" type="button" aria-label="关闭任务面板" title="关闭任务面板" onClick={() => { setManagementOpen(false); setManagementMaximized(false); updateInspectorLocation() }}><PanelRightClose size={17} /></button></span></header>
        <nav aria-label="任务面板导航">
          {([['run', '运行'], ['files', '文件'], ['changes', '变更'], ['security', '安全'], ['trace', '追踪']] as Array<[string, string]>).map(([value, label]) => <button className={managementTab === value ? 'active' : ''} type="button" key={value} onClick={() => selectInspectorTab(value)}>{label}</button>)}
          <button className={managementMoreOpen || ['tools', 'context', 'usage', 'overview', 'tasks', 'activity', 'plan', 'agents', 'project_usage'].includes(managementTab) ? 'active' : ''} type="button" aria-expanded={managementMoreOpen} onClick={() => setManagementMoreOpen((value) => !value)}><MoreHorizontal size={14} />更多</button>
        </nav>
        {managementMoreOpen ? <div className="panel-secondary-nav" aria-label="更多任务与项目视图">
          {([['context', '上下文'], ['usage', '用量'], ['tools', '工具'], ['overview', '项目'], ['tasks', '任务'], ['activity', '动态'], ['plan', '计划'], ['agents', '智能体'], ['project_usage', '项目用量']] as Array<[string, string]>).map(([value, label]) => <button className={managementTab === value ? 'active' : ''} type="button" key={value} onClick={() => { selectInspectorTab(value); setManagementMoreOpen(false) }}>{label}</button>)}
        </div> : null}
        <div className="panel-content">
          {managementTab === 'run' ? <InspectorRun task={inspectedTask} events={inspectedRunEvents.data.items} usage={inspectedUsage.data} artifacts={inspectedArtifactVersions} agentTeam={inspectedAgentTeam.data} /> : null}
          {managementTab === 'files' ? <FilesInspector projectId={projectId} task={inspectedTask} section={filesSection} onSectionChange={setFilesSection} /> : null}
          {managementTab === 'context' ? <InspectorContext projectModel={project.data.default_model} events={inspectedRunEvents.data.items} /> : null}
          {managementTab === 'tools' ? <InspectorEvents events={inspectedRunEvents.data.items.filter((item) => item.event_type.includes('tool'))} empty="当前 Run 尚无工具事件。" /> : null}
          {managementTab === 'security' ? <InspectorSecurity projectId={projectId} taskId={inspectedTask?.id} approvals={inspectedApprovals.data?.items ?? []} /> : null}
          {managementTab === 'changes' ? <InspectorChanges task={inspectedTask} artifacts={inspectedArtifactVersions} scene={project.data.scene} /> : null}
          {managementTab === 'trace' ? <InspectorEvents events={inspectedRunEvents.data.items} empty="当前 Run 尚无事件。" /> : null}
          {managementTab === 'usage' ? <InspectorUsage usage={inspectedUsage.data} /> : null}
          {managementTab === 'overview' ? <div className="project-overview-list"><button type="button" onClick={() => setSettingsOpen(true)}><Settings2 size={16} /><span><strong>项目设置</strong><small>{project.data.description || '补充项目目标与模型策略'}</small></span><ChevronRight size={15} /></button><div><Bot size={16} /><span><strong>默认智能体</strong><small>Oktapod Generalist</small></span></div><div><Sparkles size={16} /><span><strong>模型策略</strong><small>{project.data.default_model || '由 Provider 自动选择'}</small></span></div><div><LockKeyhole size={16} /><span><strong>可见范围</strong><small>仅当前用户</small></span></div></div> : null}
          {managementTab === 'tasks' ? <TasksView tasks={tasks.data?.items ?? []} loading={tasks.isLoading} /> : null}
          {managementTab === 'activity' ? <ActivityView activity={activity.data?.items ?? []} /> : null}
          {managementTab === 'plan' ? <PlanView projectId={projectId} planItems={planItems.data?.items ?? []} /> : null}
          {managementTab === 'agents' ? <AgentView key={composerDraft?.kind === 'agent' ? JSON.stringify(composerDraft) : 'agent-default'} projectId={projectId} agents={agents.data?.items ?? []} models={models.data?.items ?? []} modelsError={models.error?.message} draft={composerDraft?.kind === 'agent' ? composerDraft : undefined} /> : null}
          {managementTab === 'project_usage' ? <UsageView usage={usage.data} loading={usage.isLoading} error={usage.error?.message} /> : null}
        </div>
      </aside> : null}

      {project.data.kind === 'team' ? <MemberDialog projectId={projectId} isTeam open={membersOpen} onOpenChange={setMembersOpen} /> : null}
      <ProjectSettingsDialog project={project.data} models={models.data?.items ?? []} providerDefaultModel={models.data?.default_model ?? ''} modelsError={models.error?.message} open={settingsOpen} onOpenChange={setSettingsOpen} />
    </div>
  )
}

function ProjectSettingsDialog({ project, models, providerDefaultModel, modelsError, open, onOpenChange }: { project: Awaited<ReturnType<typeof api.project>>; models: { id: string; provider_id?: string; provider_name?: string }[]; providerDefaultModel: string; modelsError?: string; open: boolean; onOpenChange: (open: boolean) => void }) {
  const queryClient = useQueryClient()
  const [defaultModel, setDefaultModel] = useState(project.default_model)
  const [fallbackModel, setFallbackModel] = useState(project.fallback_model)
  const [providerId, setProviderId] = useState(project.model_provider_id)
  const [budgetLimited, setBudgetLimited] = useState(project.max_model_calls > 0 || project.max_tokens > 0)
  const [maxModelCalls, setMaxModelCalls] = useState(project.max_model_calls || TASK_RUNTIME_DEFAULTS.maxModelCalls)
  const [maxTokens, setMaxTokens] = useState(project.max_tokens || TASK_RUNTIME_DEFAULTS.maxTokens)
  const providerOptions = Array.from(new Map(models.filter((model) => model.provider_id).map((model) => [model.provider_id!, model.provider_name || model.provider_id!])).entries())
  const modelOptions = Array.from(new Set([project.default_model, project.fallback_model, ...models.filter((model) => !providerId || model.provider_id === providerId).map((model) => model.id)].filter(Boolean)))
  const update = useMutation({
    mutationFn: () => api.updateProjectSettings(project.id, { model_provider_id: providerId, default_model: defaultModel.trim(), fallback_model: fallbackModel.trim(), max_model_calls: budgetLimited ? maxModelCalls : 0, max_tokens: budgetLimited ? maxTokens : 0 }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['project', project.id] })
      onOpenChange(false)
    },
  })
  const budgetInvalid = budgetLimited && (maxModelCalls < 1 || maxModelCalls > 1000 || maxTokens < 1 || maxTokens > 10_000_000)
  return <Dialog.Root open={open} onOpenChange={(next) => { if (next) { setProviderId(project.model_provider_id); setDefaultModel(project.default_model); setFallbackModel(project.fallback_model); setBudgetLimited(project.max_model_calls > 0 || project.max_tokens > 0); setMaxModelCalls(project.max_model_calls || TASK_RUNTIME_DEFAULTS.maxModelCalls); setMaxTokens(project.max_tokens || TASK_RUNTIME_DEFAULTS.maxTokens) } onOpenChange(next) }}>
    <Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><Dialog.Content className="dialog-content">
      <div className="dialog-heading"><div><Dialog.Title>项目配置</Dialog.Title><Dialog.Description>Owner 或 Approver 可更新模型策略；已创建的 Run 仍使用自己的不可变快照。</Dialog.Description></div><Dialog.Close className="icon-button" aria-label="关闭">×</Dialog.Close></div>
      <form onSubmit={(event) => { event.preventDefault(); update.mutate() }}>
        <label className="form-field"><span>模型服务</span><select aria-label="项目模型服务" value={providerId} onChange={(event) => { setProviderId(event.target.value); setDefaultModel(''); setFallbackModel('') }}>{providerOptions.map(([id, name]) => <option key={id} value={id}>{name}</option>)}</select></label>
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
    onMutate: async ({ id, status }) => {
      const queryKey = ['plan-items', projectId] as const
      await queryClient.cancelQueries({ queryKey })
      const previous = queryClient.getQueryData<{ items: PlanItem[] }>(queryKey)
      queryClient.setQueryData<{ items: PlanItem[] }>(queryKey, (current) => current ? {
        ...current,
        items: current.items.map((item) => item.id === id ? { ...item, status } : item),
      } : current)
      return { previous }
    },
    onError: (_error, _variables, context) => {
      if (context?.previous) queryClient.setQueryData(['plan-items', projectId], context.previous)
    },
    onSettled: async () => queryClient.invalidateQueries({ queryKey: ['plan-items', projectId] }),
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

export function ScheduleView({ projectId, schedules, draft }: { projectId: string; schedules: Awaited<ReturnType<typeof api.schedules>>['items']; draft?: Extract<ComposerDraft, { kind: 'schedule' }> }) {
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

export function CapabilityView({ projectId, capabilities, draft, onUseSkill }: { projectId: string; capabilities: Awaited<ReturnType<typeof api.capabilities>>['items']; draft?: Extract<ComposerDraft, { kind: 'capability' }>; onUseSkill: (skill: Capability) => void }) {
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

export function MemoryView({ projectId, memories, query, onQueryChange }: { projectId: string; memories: Awaited<ReturnType<typeof api.memories>>['items']; query: string; onQueryChange: (query: string) => void }) {
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

export function TeamDefinitionView({ projectId, teams, draft }: { projectId: string; teams: TeamDefinition[]; draft?: Extract<ComposerDraft, { kind: 'team' }> }) {
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

function FilesInspector({ projectId, task, section, onSectionChange }: { projectId: string; task?: Awaited<ReturnType<typeof api.tasks>>['items'][number]; section: 'workspace' | 'project' | 'outputs'; onSectionChange: (section: 'workspace' | 'project' | 'outputs') => void }) {
  const [outputScope, setOutputScope] = useState<'current' | 'project'>('current')
  const [outputVersions, setOutputVersions] = useState<'latest' | 'all'>('latest')
  const includeHistory = outputVersions === 'all'
  const allOutputs = outputScope === 'project'
  const workspace = useQuery({ queryKey: ['workspace-files', projectId], queryFn: () => api.workspaceFiles(projectId), enabled: section === 'workspace' || section === 'outputs', refetchInterval: section === 'workspace' ? 3_000 : false })
  const outputs = useQuery({ queryKey: ['project-artifacts', projectId, allOutputs ? '' : task?.run_id, includeHistory], queryFn: () => api.projectArtifacts(projectId, { runId: allOutputs ? undefined : task?.run_id, includeHistory }), enabled: section === 'outputs' })
  const currentOutputs = useQuery({ queryKey: ['project-artifacts', projectId, task?.run_id, true], queryFn: () => api.projectArtifacts(projectId, { runId: task?.run_id, includeHistory: true }), enabled: Boolean(task?.run_id) && (section === 'workspace' || (section === 'outputs' && allOutputs)) })
  const rootProjectFiles = useQuery({ queryKey: ['assets', projectId, '', '', ''], queryFn: () => api.assets(projectId), enabled: section === 'outputs' })
  const currentArtifactNames = new Set(((section === 'workspace' || allOutputs) ? currentOutputs.data?.items : outputs.data?.items)?.map((artifact) => artifact.name) ?? [])
  const savedArtifactIds = new Set((rootProjectFiles.data?.items ?? []).filter((asset) => asset.source_type === 'artifact').map((asset) => asset.source_artifact_id))
  const uncapturedPDFs = (workspace.data?.items ?? []).filter((file) => file.kind === 'file' && file.path.toLowerCase().endsWith('.pdf') && !currentArtifactNames.has(file.path))
  const workspaceGroups = groupWorkspaceFiles(workspace.data?.items ?? [])
  return <div className="files-inspector">
    <nav className="files-inspector-tabs" aria-label="文件类型">
      {([['workspace', '工作文件'], ['project', '项目资料'], ['outputs', '任务输出']] as const).map(([value, label]) => <button type="button" className={section === value ? 'active' : ''} key={value} onClick={() => onSectionChange(value)}>{label}</button>)}
    </nav>
    {section === 'workspace' ? <section className="files-inspector-section workspace"><header><div><span className="file-lifecycle-label">可编辑 · 按生命周期归类</span><strong>工作文件</strong><small>保留项目原有目录约定；Agent 的过程与临时文件单独折叠，最终文件登记后才成为不可变任务输出。</small></div><button type="button" aria-label="刷新工作文件" title="刷新工作文件" onClick={() => workspace.refetch()}><RefreshCw size={14} /></button></header>{workspace.error ? <div className="panel-error">{workspace.error.message}</div> : null}<div className="workspace-lifecycle-groups">{workspaceLifecycleOrder.map((role) => <WorkspaceLifecycleGroup currentArtifactNames={currentArtifactNames} files={workspaceGroups[role]} key={role} projectId={projectId} role={role} runId={task?.run_id} />)}</div>{workspace.isLoading ? <div className="inspector-empty compact"><Folder size={16} /><strong>正在读取工作文件…</strong></div> : null}{!workspace.isLoading && workspace.data?.items.length === 0 ? <div className="inspector-empty compact"><Folder size={16} /><strong>工作文件暂无内容</strong></div> : null}</section> : null}
    {section === 'project' ? <section className="files-inspector-section project"><header><div><span className="file-lifecycle-label">可复用输入</span><strong>项目资料</strong><small>上传的资料，或从任务输出明确保存的成果；供后续任务作为附件使用，不等于任务交付。</small></div></header><AssetsView projectId={projectId} compact /></section> : null}
    {section === 'outputs' ? <section className="files-inspector-section outputs"><header><div><span className="file-lifecycle-label">不可变</span><strong>任务输出</strong><small>{allOutputs ? '查看这个项目所有任务登记的输出' : task ? `本次任务 · Run ${task.run_id.slice(0, 12)}` : '当前会话尚无任务'}</small></div></header><div className="files-output-filters" aria-label="任务输出筛选"><label><span>范围</span><select aria-label="任务输出范围" value={outputScope} onChange={(event) => setOutputScope(event.target.value as 'current' | 'project')}><option value="current">本次任务</option><option value="project">项目全部任务</option></select></label><label><span>版本</span><select aria-label="任务输出版本" value={outputVersions} onChange={(event) => setOutputVersions(event.target.value as 'latest' | 'all')}><option value="latest">仅最新版本</option><option value="all">包含旧版本</option></select></label></div>{!allOutputs && task?.run_id && uncapturedPDFs.length ? <div className="files-recovery-card"><div><strong>发现尚未保存的 PDF</strong><small>文件已生成在工作区，但这个旧任务没有登记为输出。确认后会归属到本次任务。</small></div>{uncapturedPDFs.map((file) => <WorkspaceCaptureAction file={file} key={file.path} projectId={projectId} runId={task.run_id} />)}</div> : null}{outputs.error ? <div className="panel-error">{outputs.error.message}</div> : null}<div className="files-inspector-list">{outputs.data?.items.map((artifact) => <TaskOutputRow artifact={artifact} key={artifact.id} saved={savedArtifactIds.has(artifact.id)} />)}</div>{outputs.isLoading ? <div className="inspector-empty compact"><FileBox size={16} /><strong>正在读取任务输出…</strong></div> : null}{!outputs.isLoading && outputs.data?.items.length === 0 ? <div className="inspector-empty compact"><FileBox size={16} /><strong>这个范围还没有任务输出</strong><span>{uncapturedPDFs.length ? '上方检测到工作区 PDF，可以明确保存到本次任务。' : '脚本生成的文件需要登记后才会成为不可变任务输出。'}</span></div> : null}</section> : null}
  </div>
}

type WorkspaceLifecycleRole = WorkspaceFile['lifecycle_role']

const workspaceLifecycleOrder: WorkspaceLifecycleRole[] = ['source', 'input', 'candidate_output', 'working', 'temporary']
const workspaceLifecycleMeta: Record<WorkspaceLifecycleRole, { label: string; copy: string; collapsed: boolean }> = {
  source: { label: '项目内容', copy: '跟随项目目录约定的源码与可编辑内容', collapsed: false },
  input: { label: '输入与下载', copy: '任务读取的外部资料；长期复用建议保存到项目资料', collapsed: false },
  candidate_output: { label: '待登记输出', copy: '仍可变；保存后才进入本次任务的不可变输出', collapsed: false },
  working: { label: '过程文件', copy: '研究、图表与生成脚本，默认收起以减少干扰', collapsed: true },
  temporary: { label: '临时辅助', copy: '兼容识别的提取脚本、日志与缓存，不作为交付物', collapsed: true },
}

function groupWorkspaceFiles(files: WorkspaceFile[]) {
  const grouped: Record<WorkspaceLifecycleRole, WorkspaceFile[]> = { source: [], input: [], working: [], temporary: [], candidate_output: [] }
  files.forEach((file) => grouped[file.lifecycle_role ?? 'source'].push(file))
  return grouped
}

function WorkspaceLifecycleGroup({ currentArtifactNames, files, projectId, role, runId }: { currentArtifactNames: Set<string>; files: WorkspaceFile[]; projectId: string; role: WorkspaceLifecycleRole; runId?: string }) {
  if (files.length === 0) return null
  const meta = workspaceLifecycleMeta[role]
  const rows = <div className="files-inspector-list">{files.map((file) => <WorkspaceFileRow currentArtifactNames={currentArtifactNames} file={file} key={file.path} projectId={projectId} runId={runId} />)}</div>
  if (meta.collapsed) return <details className={`workspace-lifecycle-group ${role}`}><summary><span><strong>{meta.label}</strong><small>{meta.copy}</small></span><em>{files.length}</em><ChevronDown size={14} /></summary>{rows}</details>
  return <section className={`workspace-lifecycle-group ${role}`}><header><span><strong>{meta.label}</strong><small>{meta.copy}</small></span><em>{files.length}</em></header>{rows}</section>
}

function legacySafeMessageContent(message: ConversationMessage, linkedTaskStatus?: string) {
  if (message.role === 'assistant' && linkedTaskStatus === 'failed' && /通过确定性校验/.test(message.content)) {
    return '最后一个工具步骤已完成，但任务整体已停止。已生成的文件和执行收据仍然保留，请查看下方失败原因。'
  }
  return message.content
}

function MessagePresentation({ presentation }: { presentation?: ConversationMessage['presentation'] }) {
  const attachments = presentation?.attachments ?? []
  const skill = presentation?.skill
  if (attachments.length === 0 && !skill && !presentation?.queue_kind) return null
  return <div className="message-presentation" aria-label="本次发送上下文">
    {skill ? <span className="message-context-chip skill" title={`Skill ${skill.name} v${skill.version}`}><Puzzle size={13} /><strong>{skill.command || `/${skill.name}`}</strong>{skill.args ? <small>{skill.args}</small> : null}</span> : null}
    {attachments.map((attachment) => <a className="message-context-chip attachment" href={api.assetContentUrl(attachment.id)} target="_blank" rel="noreferrer" key={attachment.id} title={`打开附件 ${attachment.name}`}><FileIcon size={13} /><span><strong>{attachment.name}</strong><small>{formatBytes(attachment.size_bytes)}</small></span></a>)}
    {presentation?.queue_kind ? <span className="message-queue-state"><Clock3 size={12} />{presentation.queue_kind === 'steering' ? '已发送 · 引导当前任务' : '已发送 · 排到下一轮'}</span> : null}
  </div>
}

function WorkspaceFileRow({ currentArtifactNames, file, projectId, runId }: { currentArtifactNames: Set<string>; file: WorkspaceFile; projectId: string; runId?: string }) {
  return <div><span>{file.kind === 'directory' ? <Folder size={15} /> : <FileIcon size={15} />}<span>{file.kind === 'file' ? <a href={api.workspaceContentUrl(projectId, file.path)} target="_blank" rel="noreferrer">{file.path}</a> : <strong>{file.path}</strong>}<small>{file.git_status || '未变更'} · {formatBytes(file.size_bytes)} · {new Date(file.modified_at).toLocaleString('zh-CN')}</small></span></span><span className="files-row-actions">{file.kind === 'file' && file.lifecycle_role === 'candidate_output' && runId && !currentArtifactNames.has(file.path) ? <WorkspaceCaptureAction compact file={file} projectId={projectId} runId={runId} /> : null}{file.kind === 'file' ? <a aria-label={`打开 ${file.path}`} href={api.workspaceContentUrl(projectId, file.path)} target="_blank" rel="noreferrer"><ChevronRight size={14} /></a> : null}</span></div>
}

function WorkspaceCaptureAction({ compact = false, file, projectId, runId }: { compact?: boolean; file: WorkspaceFile; projectId: string; runId: string }) {
  const queryClient = useQueryClient()
  const capture = useMutation({ mutationFn: () => api.captureWorkspaceArtifact(projectId, { run_id: runId, path: file.path, kind: 'deliverable' }), onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['project-artifacts', projectId] }) })
  const label = capture.isSuccess ? '已保存到任务输出' : capture.isPending ? '正在保存到任务输出' : '保存为本次任务输出'
  return <span className={compact ? 'workspace-capture-action compact' : 'workspace-capture-action'}>{compact ? null : <span><FileBox size={14} /><strong>{file.path}</strong></span>}<button className="file-icon-action" type="button" aria-label={`${label}：${file.path}`} title={`${label}；保存后成为本次任务的不可变输出`} disabled={capture.isPending || capture.isSuccess} onClick={() => capture.mutate()}><FileCheck2 size={15} /></button>{capture.error ? <small>{capture.error.message}</small> : null}</span>
}

function TaskOutputRow({ artifact, saved }: { artifact: Artifact; saved: boolean }) {
  const queryClient = useQueryClient()
  const save = useMutation({ mutationFn: () => api.saveArtifactAsProjectFile(artifact.id, { name: artifact.name, visibility: 'private' }), onSuccess: () => queryClient.invalidateQueries({ queryKey: ['assets', artifact.project_id] }) })
  const alreadySaved = saved || save.isSuccess
  const saveLabel = alreadySaved ? '已保存到项目资料' : save.isPending ? '正在保存到项目资料' : '保存到项目资料'
  return <div><span><FileIcon size={15} /><span><a href={api.artifactPreviewUrl(artifact.id)} target="_blank" rel="noreferrer">{artifact.name}</a><small>{artifact.kind === 'evidence' ? '过程证据' : '交付物'} · {artifact.media_type} · {formatBytes(artifact.size_bytes)}</small></span></span><span className="files-row-actions"><button className="file-icon-action" type="button" aria-label={`${saveLabel}：${artifact.name}`} title={`${saveLabel}；供本项目后续任务复用，不会发布到项目外`} disabled={save.isPending || alreadySaved} onClick={() => save.mutate()}><FolderInput size={15} /></button><a aria-label={`下载 ${artifact.name}`} title={`下载 ${artifact.name}`} href={api.artifactContentUrl(artifact.id)}><Download size={14} /></a></span></div>
}

function AssetsView({ projectId, compact = false }: { projectId: string; compact?: boolean }) {
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

  return <div className={`asset-surface${compact ? ' compact' : ''}`}>
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
      {!assets.isLoading && assets.data?.items.length === 0 ? <div className="asset-empty"><FileBox size={26} /><strong>{search ? '没有匹配的项目资料' : '当前目录为空'}</strong><small>{search ? '调整搜索词或类型筛选。' : '上传资料，或从任务输出保存可复用成果。'}</small></div> : null}
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

function TaskComposer({ projectId, conversationId, activeTask, agents, skills, providers, models, projectModelProviderId, projectDefaultModel, projectScene, autoReviewAvailable, autoReviewReason, initialValue, onComposerDraft, onResourceInstallDraft, onConversationSelected, onOptimisticMessage, onRoute, onDelta, onFailed, onSettled }: { projectId: string; conversationId?: string; activeTask?: Awaited<ReturnType<typeof api.tasks>>['items'][number]; agents: Awaited<ReturnType<typeof api.agents>>['items']; skills: Capability[]; providers: ProviderInstance[]; models: Array<{ id: string; provider_id?: string; tool_calling?: boolean }>; projectModelProviderId: string; projectDefaultModel: string; projectScene: Scene; autoReviewAvailable?: boolean; autoReviewReason?: string; initialValue?: string; onComposerDraft: (draft: ComposerDraft) => void; onResourceInstallDraft: (draft: ResourceInstallDraft, conversationId: string) => void; onConversationSelected: (id: string) => void; onOptimisticMessage: (clientMessageId: string, content: string, presentation: ConversationMessage['presentation']) => void; onRoute: (route: 'chat' | 'task' | 'resource') => void; onDelta: (delta: string) => void; onFailed: (clientMessageId: string) => void; onSettled: (clientMessageId: string) => void }) {
  const bootstrap = useBootstrap()
  const storageKey = `oktapod.composer.${bootstrap.user.id}.${projectId}.${conversationId ?? 'new'}`
  const permissionStorageKey = `oktapod.permission-mode.${bootstrap.user.id}.${projectId}`
  const modelStorageKey = `oktapod.model-selection.${bootstrap.user.id}.${projectId}`
  const treePrefillKey = conversationId ? `oktapod.composer.prefill.${conversationId}` : ''
  const newTaskPrefillKey = !conversationId ? `oktapod.new-task-prefill.${projectId}` : ''
  const [goal, setGoal] = useState(() => {
    const value = initialValue ?? (treePrefillKey ? window.sessionStorage.getItem(treePrefillKey) : undefined) ?? (newTaskPrefillKey ? window.sessionStorage.getItem(newTaskPrefillKey) : undefined) ?? window.localStorage.getItem(storageKey) ?? ''
    if (newTaskPrefillKey) window.sessionStorage.removeItem(newTaskPrefillKey)
    return value
  })
  const [mode, setMode] = useState<TaskMode>(initialValue ? 'single_agent' : 'auto')
  const [permissionMode, setPermissionMode] = useState<PermissionMode | undefined>(() => {
    const saved = window.localStorage.getItem(permissionStorageKey)
    return saved === 'ask' || saved === 'auto_review' || saved === 'full_access' ? saved : undefined
  })
  const [fullAccessOpen, setFullAccessOpen] = useState(false)
  const [scene, setScene] = useState<Scene>(projectScene)
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [agentId, setAgentId] = useState('default')
  const [providerId, setProviderId] = useState(() => { const saved = readModelSelection(modelStorageKey).providerId; return saved === 'default' && projectModelProviderId ? projectModelProviderId : saved })
  const [model, setModel] = useState(() => readModelSelection(modelStorageKey).model)
  const [thinking, setThinking] = useState<'auto' | 'off' | 'low' | 'medium' | 'high'>('auto')
  const [skillCommandError, setSkillCommandError] = useState('')
  const [attachments, setAttachments] = useState<ComposerAttachment[]>([])
  const [attachmentPickerOpen, setAttachmentPickerOpen] = useState(false)
  const [assetQuery, setAssetQuery] = useState('')
  const [selectedSkillIndex, setSelectedSkillIndex] = useState(0)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const attachmentInputRef = useRef<HTMLInputElement>(null)
  const queryClient = useQueryClient()
  const preferences = useQuery({ queryKey: ['preferences'], queryFn: api.preferences, staleTime: 60_000 })
  const resolvedPermissionMode = permissionMode ?? preferences.data?.default_permission ?? 'ask'
  const effectivePermissionMode = autoReviewAvailable === false && resolvedPermissionMode === 'auto_review' ? 'ask' : resolvedPermissionMode
  const projectAssets = useQuery({ queryKey: ['assets', projectId, 'composer'], queryFn: () => api.assets(projectId, { kind: 'file' }), enabled: attachmentPickerOpen, staleTime: 5_000 })
  const providerModels = useQuery({ queryKey: ['provider-models', providerId], queryFn: () => api.providerModels(providerId), enabled: providerId !== 'default', retry: false, staleTime: 60_000 })
  const submit = useMutation({
    mutationFn: ({ content, invocation, queueKind, forcedMode, contextAssetIds }: { clientMessageId: string; content: string; invocation: ReturnType<typeof parseSkillInvocation>; queueKind: 'follow_up' | 'steering'; forcedMode?: TaskMode; contextAssetIds: string[] }) => api.streamProjectTurn(projectId, { conversation_id: conversationId, content, mode: forcedMode ?? (isCompactCommand(content) ? 'answer' : invocation ? 'single_agent' : mode), visibility: 'private', permission_mode: effectivePermissionMode, queue_kind: queueKind, scene, provider_id: providerId === 'default' ? undefined : providerId, model: selectedModel || undefined, thinking, agent_id: agentId === 'default' ? undefined : agentId, skill_id: invocation?.skillId, skill_args: invocation?.skillArgs, context_asset_ids: contextAssetIds }, { onRoute, onDelta }),
    onSuccess: async (result, variables) => {
      if (result.resource_install_draft) onResourceInstallDraft(result.resource_install_draft, result.conversation.id)
      else onConversationSelected(result.conversation.id)
      await queryClient.invalidateQueries({ queryKey: ['tasks', projectId] })
      await queryClient.invalidateQueries({ queryKey: ['project-activity', projectId] })
      await queryClient.invalidateQueries({ queryKey: ['conversations', projectId] })
      await queryClient.invalidateQueries({ queryKey: ['conversation-messages', result.conversation.id] })
      await queryClient.invalidateQueries({ queryKey: ['plan-items', projectId] })
      onSettled(variables.clientMessageId)
    },
    onError: (_error, variables) => onFailed(variables.clientMessageId),
  })
  const abort = useMutation({
    mutationFn: () => api.controlTask(activeTask!.id, 'cancel'),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['tasks', projectId] }),
        queryClient.invalidateQueries({ queryKey: ['run-plan', activeTask?.run_id] }),
        queryClient.invalidateQueries({ queryKey: ['project-activity', projectId] }),
      ])
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

  const uploadAttachment = (attachment: ComposerAttachment) => {
    if (!attachment.file) return
    setAttachments((current) => current.map((item) => item.key === attachment.key ? { ...item, status: 'uploading', progress: 0, error: undefined } : item))
    void api.uploadAssetWithProgress(projectId, attachment.file, 'private', (progress) => {
      setAttachments((current) => current.map((item) => item.key === attachment.key ? { ...item, progress } : item))
    }).then((asset) => {
      setAttachments((current) => current.map((item) => item.key === attachment.key ? { ...item, asset, status: 'ready', progress: 100 } : item))
      void queryClient.invalidateQueries({ queryKey: ['assets', projectId] })
    }).catch((error: unknown) => {
      setAttachments((current) => current.map((item) => item.key === attachment.key ? { ...item, status: 'failed', error: error instanceof Error ? error.message : '附件上传失败' } : item))
    })
  }

  const addAttachments = (files: FileList | null) => {
    if (!files?.length) return
    const available = Math.max(0, 8 - attachments.length)
    const selected = Array.from(files).slice(0, available).map((file) => ({ key: crypto.randomUUID(), file, name: file.name, size: file.size, source: 'upload' as const, status: 'uploading' as const, progress: 0 }))
    if (selected.length === 0) {
      setSkillCommandError('每次最多选择 8 个附件。')
      return
    }
    setAttachments((current) => [...current, ...selected])
    selected.forEach(uploadAttachment)
    if (files.length > available) setSkillCommandError('每次最多选择 8 个附件，超出的文件未加入。')
  }

  const attachProjectAsset = (asset: Asset) => {
    if (attachments.some((item) => item.asset?.id === asset.id)) {
      setAttachmentPickerOpen(false)
      return
    }
    if (attachments.length >= 8) {
      setSkillCommandError('每次最多选择 8 个附件。')
      return
    }
    setAttachments((current) => [...current, {
      key: crypto.randomUUID(), asset, name: asset.name, size: asset.size_bytes,
      source: 'project', status: 'ready', progress: 100,
    }])
    setAttachmentPickerOpen(false)
    setSkillCommandError('')
  }

  const send = (queueKind: 'follow_up' | 'steering' = activeTask ? 'steering' : 'follow_up') => {
	if (!goal.trim() || (submit.isPending && !activeTask)) return
	if (attachments.some((item) => item.status === 'uploading')) { setSkillCommandError('附件仍在上传，请等待完成后发送。'); return }
	if (attachments.some((item) => item.status === 'failed')) { setSkillCommandError('有附件上传失败，请重试或移除后发送。'); return }
    const content = goal.trim()
    const draft = parseComposerDraft(content)
    if (draft?.kind === 'agent') { onComposerDraft(draft); setGoal(''); return }
    const invocation = parseSkillInvocation(content, skills)
    if (content.startsWith('/') && !invocation && !isCompactCommand(content)) {
      setSkillCommandError('没有找到可用命令，请从列表选择。')
      return
    }
    setGoal('')
    window.localStorage.removeItem(storageKey)
    if (treePrefillKey) window.sessionStorage.removeItem(treePrefillKey)
    setSkillCommandError('')
	// Attachments are immutable Run context. When a task is already active,
	// route this message to the next Run instead of mutating the current snapshot.
    const effectiveQueueKind = attachments.length > 0 && activeTask ? 'follow_up' : queueKind
    const clientMessageId = `pending-${crypto.randomUUID()}`
    const readyAttachments = attachments.filter((item) => item.status === 'ready' && item.asset)
    const contextAssetIds = readyAttachments.map((item) => item.asset!.id)
    onOptimisticMessage(clientMessageId, content, {
      attachments: readyAttachments.map((item) => ({
        id: item.asset!.id, name: item.asset!.name, media_type: item.asset!.media_type,
        size_bytes: item.asset!.size_bytes, content_hash: item.asset!.content_hash,
      })),
      skill: invocation ? { id: invocation.skillId, name: invocation.skillName, version: skills.find((item) => item.id === invocation.skillId)?.version ?? '', command: content.split(/\s+/, 1)[0] ?? '', args: invocation.skillArgs } : undefined,
      queue_kind: activeTask ? effectiveQueueKind : undefined,
    })
    setAttachments([])
    submit.mutate({ clientMessageId, content, invocation, queueKind: effectiveQueueKind, contextAssetIds })
  }

  const enabledAgents = agents.filter((agent) => agent.status === 'enabled')
  const executorItems: [string, string][] = [['default', '默认 Agent'], ...enabledAgents.map((agent) => [agent.id, agent.name] as [string, string])]
  const providerItems: [string, string][] = [
    ['default', '全部模型服务'],
    ...providers.filter((provider) => provider.status === 'active').map((provider) => [provider.provider_id, provider.display_name] as [string, string]),
  ]
  const selectedProvider = providers.find((provider) => provider.provider_id === providerId)
  type ToolSupport = 'yes' | 'no' | 'unknown'
  const catalog: Array<{ id: string; toolSupport: ToolSupport }> = providerId === 'default'
    ? models.map((item) => ({ id: item.id, toolSupport: item.tool_calling === true ? 'yes' : item.tool_calling === false ? 'no' : 'unknown' }))
    : (providerModels.data?.items ?? []).filter((item) => item.enabled).map((item) => ({
      id: item.model_id,
      toolSupport: item.capabilities.tool_calling_known === true
        ? item.capabilities.tool_calling === false ? 'no' as const : 'yes' as const
        : 'unknown' as const,
    }))
  const fallbackModel = providerId === 'default'
    ? projectDefaultModel || providers.find((provider) => provider.is_default)?.default_model_id
    : selectedProvider?.default_model_id
  const selectedModel = model && catalog.some((item) => item.id === model) ? model : ''
  const modelItems: Array<[string, string, boolean?]> = [
    ['default', fallbackModel ? `模型 · 默认（${fallbackModel}）` : '模型 · 默认'],
    ...catalog.map((item) => [item.id, `模型 · ${item.id}${item.toolSupport === 'no' ? '（不支持工具）' : ''}`, mode === 'single_agent' && item.toolSupport === 'no'] as [string, string, boolean]),
  ]
  const selectProvider = (value: string) => {
    setProviderId(value)
    setModel('')
    window.localStorage.setItem(modelStorageKey, JSON.stringify({ providerId: value, model: '' }))
    persistRecentSelection(value === 'default' ? '' : value, '')
  }
  const selectModel = (value: string) => {
    const selected = value === 'default' ? '' : value
    setModel(selected)
    window.localStorage.setItem(modelStorageKey, JSON.stringify({ providerId, model: selected }))
    persistRecentSelection(providerId === 'default' ? '' : providerId, selected)
  }
  const persistRecentSelection = (recentProviderID: string, recentModelID: string) => {
    if (!preferences.data) return
    void api.updatePreferences({
      theme: preferences.data.theme, density: preferences.data.density,
      default_permission: preferences.data.default_permission,
      recent_provider_id: recentProviderID, recent_model_id: recentModelID,
    }).then(() => queryClient.invalidateQueries({ queryKey: ['preferences'] })).catch(() => undefined)
  }
  const selectPermissionMode = (value: PermissionMode) => {
    setPermissionMode(value)
    window.localStorage.setItem(permissionStorageKey, value)
  }
  const availableProjectAssets = (projectAssets.data?.items ?? []).filter((asset) => asset.kind === 'file' && (!assetQuery.trim() || asset.name.toLowerCase().includes(assetQuery.trim().toLowerCase())))

  return <div className="conversation-composer-wrap">
    {skillCommandError || submit.error || abort.error ? <div className="composer-error" role="alert">{skillCommandError || submit.error?.message || abort.error?.message}</div> : null}
    {commandSuggestions.length > 0 ? <div className="skill-command-menu" role="listbox" aria-label="Skills">
      <div className="skill-command-menu-heading"><Sparkles size={14} /><span>命令</span><small>{commandSuggestions.length}</small></div>
      {commandSuggestions.map((suggestion, index) => suggestion.kind === 'system'
        ? <button type="button" role="option" aria-selected={activeSkillIndex === index} className={activeSkillIndex === index ? 'active' : ''} key="system-compact" onMouseDown={(event) => event.preventDefault()} onClick={() => chooseCommand(suggestion)}><span className="skill-command-icon"><Workflow size={15} /></span><span><strong>/compact</strong><small>{suggestion.description}</small></span><em>系统</em></button>
        : <button type="button" role="option" aria-selected={activeSkillIndex === index} className={activeSkillIndex === index ? 'active' : ''} key={suggestion.skill.id} onMouseDown={(event) => event.preventDefault()} onClick={() => chooseCommand(suggestion)}><span className="skill-command-icon"><Puzzle size={15} /></span><span><strong>/{skillCommandName(suggestion.skill.name)}</strong><small>{skillDescription(suggestion.skill) || `Skill v${suggestion.skill.version}`}</small></span><em>v{suggestion.skill.version}</em></button>)}
    </div> : null}
    <form className="conversation-composer" noValidate onSubmit={(event) => { event.preventDefault(); send() }}>
      {attachments.length > 0 ? <div className="composer-attachments" aria-label="已选附件">{attachments.map((attachment) => <div className={`composer-attachment ${attachment.status}`} key={attachment.key}><FileIcon size={15} /><span><strong>{attachment.name}</strong><small>{attachment.status === 'uploading' ? `上传中 ${attachment.progress}%` : attachment.status === 'failed' ? attachment.error || '附件上传失败，可改为关联已有项目资料' : `${formatBytes(attachment.size)} · ${attachment.source === 'project' ? '项目资料' : '已上传'}`}</small></span>{attachment.status === 'failed' ? <button type="button" aria-label={`重试 ${attachment.name}`} title="重试上传" onClick={() => uploadAttachment(attachment)}><RefreshCw size={13} /></button> : null}{attachment.status === 'failed' ? <button type="button" aria-label="选择已有项目资料" title="关联已有项目资料" onClick={() => setAttachmentPickerOpen(true)}><FolderInput size={13} /></button> : null}<button type="button" aria-label={`移除 ${attachment.name}`} title="移除附件" onClick={() => setAttachments((current) => current.filter((item) => item.key !== attachment.key))}><X size={13} /></button></div>)}</div> : null}
      <textarea ref={textareaRef} aria-label="消息" value={goal} onChange={(event) => { setGoal(event.target.value); setSelectedSkillIndex(0); setSkillCommandError('') }} onKeyDown={(event) => {
        if (commandSuggestions.length > 0 && event.key === 'ArrowDown') { event.preventDefault(); setSelectedSkillIndex((current) => (current + 1) % commandSuggestions.length); return }
        if (commandSuggestions.length > 0 && event.key === 'ArrowUp') { event.preventDefault(); setSelectedSkillIndex((current) => (current - 1 + commandSuggestions.length) % commandSuggestions.length); return }
        if (commandSuggestions.length > 0 && event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); const selectedCommand = commandSuggestions[activeSkillIndex] ?? commandSuggestions[0]; if (selectedCommand) chooseCommand(selectedCommand); return }
        if (event.key === 'Escape' && activeTask && !abort.isPending) { event.preventDefault(); abort.mutate(); return }
        if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); send(event.altKey ? 'follow_up' : activeTask ? 'steering' : 'follow_up') }
      }} placeholder="发消息，或描述要完成的任务" rows={2} />
      {activeTask ? <p className="composer-delivery-hint">Enter 引导当前任务 · Alt+Enter 排到下一轮 · 也可直接说“暂停、继续或取消当前任务”</p> : null}
      {advancedOpen ? <div className="composer-advanced">
        <div className="composer-advanced-row"><InlineSelect value={scene} onValueChange={(value) => setScene(value as Scene)} items={[["work", "通用场景"], ["code", "代码场景"]]} /><InlineSelect value={providerId} onValueChange={selectProvider} items={providerItems} /><InlineSelect value={thinking} onValueChange={(value) => setThinking(value as typeof thinking)} items={[["auto", "Thinking · Auto"], ["off", "Thinking · Off"], ["low", "Thinking · Low"], ["medium", "Thinking · Medium"], ["high", "Thinking · High"]]} />{mode !== 'answer' && enabledAgents.length > 0 ? <InlineSelect value={agentId} onValueChange={setAgentId} items={executorItems} ariaLabel="执行 Agent" /> : null}</div>
        <p className="composer-runtime-defaults">运行默认值：模型交互最多重试 {TASK_RUNTIME_DEFAULTS.modelRetryLimit} 次、失败工具恢复最多 {TASK_RUNTIME_DEFAULTS.toolRetryLimit} 次；单次输出按 {TASK_RUNTIME_DEFAULTS.modelOutputTokens[0].toLocaleString('zh-CN')} → {TASK_RUNTIME_DEFAULTS.modelOutputTokens[1].toLocaleString('zh-CN')} → {TASK_RUNTIME_DEFAULTS.modelOutputTokens[2].toLocaleString('zh-CN')} Token 递增；上下文默认保留最近 {TASK_RUNTIME_DEFAULTS.contextRetainTokens.toLocaleString('zh-CN')} Token。</p>
        {autoReviewAvailable === false ? <p className="composer-governance-notice"><ShieldAlert size={13} />自动审查当前不可用。请在<Link to="/settings/$section" params={{ section: 'governance' }}>治理与审批</Link>中选择已启用的独立 Provider 和 Model；<Link to="/diagnostics">查看诊断原因</Link>。未可用时系统会关闭式转为人工审批。</p> : null}
      </div> : null}
      <div className="composer-toolbar"><div><button className="composer-icon" type="button" aria-label="添加附件" title="上传新文件或关联项目资料（最多 8 个）" disabled={attachments.length >= 8} onClick={() => setAttachmentPickerOpen(true)}><Paperclip size={16} /></button><input ref={attachmentInputRef} className="sr-only" type="file" multiple onChange={(event) => { addAttachments(event.target.files); event.target.value = ''; setAttachmentPickerOpen(false) }} /><InlineSelect value={mode} onValueChange={(value) => setMode(value as TaskMode)} items={[["auto", "Auto"], ["answer", "Ask"], ["single_agent", "Task"]]} ariaLabel="运行模式" /><InlineSelect value={selectedModel || 'default'} onValueChange={selectModel} items={modelItems} ariaLabel="模型" title={activeTask ? '当前 Run 使用已冻结模型；这里的选择用于下一个 Run' : '选择本次发送使用的模型'} /><InlineSelect value={effectivePermissionMode} onValueChange={(value) => { if (value === 'full_access') { setFullAccessOpen(true); return } selectPermissionMode(value as PermissionMode) }} items={[["ask", "请我审批"], ["auto_review", autoReviewAvailable === false ? "自动审查（未配置）" : "自动审查", autoReviewAvailable === false], ["full_access", "完全访问"]]} ariaLabel="权限模式" title={autoReviewAvailable === false ? `${autoReviewReason ?? ''} 请在“设置 → 治理与审批”中配置。` : undefined} /><button className={`composer-icon ${advancedOpen ? 'active' : ''}`} type="button" aria-label="执行设置" title="场景、模型服务与运行设置" onClick={() => setAdvancedOpen((value) => !value)}><SlidersHorizontal size={16} /></button></div><div className="composer-send-group">{activeTask && !goal.trim() ? <button className="composer-send stop" disabled={abort.isPending} type="button" aria-label="停止任务" title="停止任务（Esc，也可直接说“取消当前任务”）" onClick={() => abort.mutate()}><Square size={11} fill="currentColor" /></button> : <button className="composer-send" disabled={(submit.isPending && !activeTask) || !goal.trim() || attachments.some((item) => item.status === 'uploading')} type="submit" aria-label="发送" title={activeTask ? '发送并引导当前任务；Alt+Enter 排到下一轮' : '发送'}><ArrowUp size={18} /></button>}</div></div>
      {providerModels.error || (model && !selectedModel) ? <p className="composer-model-notice" role="status">{providerModels.error ? '模型目录暂时不可用，将使用该 Provider 的默认模型。' : '之前选择的模型已失效，已自动使用默认模型。'}</p> : null}
    </form>
    <Dialog.Root open={attachmentPickerOpen} onOpenChange={setAttachmentPickerOpen}>
      <Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><Dialog.Content className="dialog-content composer-asset-picker" aria-describedby="composer-asset-picker-description">
        <Dialog.Title>添加附件</Dialog.Title>
        <Dialog.Description id="composer-asset-picker-description">上传新文件，或直接关联已经存在的项目资料。关联不会复制文件，也不会因同名冲突失败。</Dialog.Description>
        <div className="composer-asset-picker-actions"><button className="primary" type="button" onClick={() => attachmentInputRef.current?.click()}><Upload size={15} />上传新文件</button><span>最多 8 个附件</span></div>
        <label className="composer-asset-search"><Search size={14} /><input value={assetQuery} onChange={(event) => setAssetQuery(event.target.value)} placeholder="搜索项目资料" /></label>
        <div className="composer-asset-list">
          {projectAssets.isLoading ? <span>正在读取项目资料…</span> : null}
          {availableProjectAssets.map((asset) => <button type="button" key={asset.id} onClick={() => attachProjectAsset(asset)}><FileIcon size={15} /><span><strong>{asset.name}</strong><small>{formatBytes(asset.size_bytes)} · {asset.media_type || '文件'}</small></span><Plus size={14} /></button>)}
          {!projectAssets.isLoading && availableProjectAssets.length === 0 ? <span>没有匹配的项目资料，可上传新文件。</span> : null}
        </div>
        <div className="dialog-actions"><Dialog.Close asChild><button type="button">取消</button></Dialog.Close></div>
      </Dialog.Content></Dialog.Portal>
    </Dialog.Root>
    <Dialog.Root open={fullAccessOpen} onOpenChange={setFullAccessOpen}>
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content className="dialog-content permission-mode-dialog" aria-describedby="full-access-description">
          <Dialog.Title>启用完全访问？</Dialog.Title>
          <Dialog.Description id="full-access-description">它会用于后续发送的任务，直到你手动切换权限模式。工作区内 L0-L2 动作免逐次审批；越界访问、确定性硬拒绝和 L3 高风险动作仍不会放行。</Dialog.Description>
          <div className="dialog-actions"><Dialog.Close asChild><button type="button">取消</button></Dialog.Close><button className="primary" type="button" onClick={() => { selectPermissionMode('full_access'); setFullAccessOpen(false) }}>确认启用</button></div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  </div>
}

function containsDraftSecret(value: string) {
  return /(?:api[_-]?key|authorization|bearer|password|secret)\s*[:=]\s*\S{8,}/i.test(value)
    || /\bsk-[a-z0-9_-]{16,}\b/i.test(value)
    || /\btvly-[a-z0-9_-]{20,}\b/i.test(value)
}

function readModelSelection(storageKey: string) {
  try {
    const saved = JSON.parse(window.localStorage.getItem(storageKey) || '{}') as { providerId?: unknown; model?: unknown }
    return { providerId: typeof saved.providerId === 'string' ? saved.providerId : 'default', model: typeof saved.model === 'string' ? saved.model : '' }
  } catch {
    return { providerId: 'default', model: '' }
  }
}

function InlineSelect({ value, onValueChange, items, ariaLabel, title }: { value: string; onValueChange: (value: string) => void; items: Array<[string, string, boolean?]>; ariaLabel?: string; title?: string }) {
  return <Select.Root value={value} onValueChange={onValueChange}><Select.Trigger className="inline-select" aria-label={ariaLabel} title={title}><Select.Value /><Select.Icon><ChevronDown size={14} /></Select.Icon></Select.Trigger><Select.Portal><Select.Content className="select-content" position="popper"><Select.Viewport>{items.map(([itemValue, label, disabled]) => <Select.Item className="select-item" value={itemValue} key={itemValue} disabled={disabled}><Select.ItemText>{label}</Select.ItemText><Select.ItemIndicator><Check size={14} /></Select.ItemIndicator></Select.Item>)}</Select.Viewport></Select.Content></Select.Portal></Select.Root>
}
