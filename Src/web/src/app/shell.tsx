import { useState, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useRouterState } from '@tanstack/react-router'
import { Activity, AlertTriangle, Check, CheckCircle2, ChevronDown, CirclePause, FileUp, Folder, GitCompareArrows, Home, LoaderCircle, LogOut, Menu, MessageSquare, MoreHorizontal, Network, PackageOpen, PanelLeftClose, PanelLeftOpen, Pencil, Plus, Search, Settings, ShieldAlert, ShieldCheck, Trash2, X } from 'lucide-react'
import { api, type Bootstrap } from '../shared/api/client'
import { assistantExcerpt } from '../shared/assistant-segments'
import { BootstrapContext } from '../shared/bootstrap-context'

export function AppShell({ bootstrap, children }: { bootstrap: Bootstrap; children: ReactNode }) {
  const [mobileOpen, setMobileOpen] = useState(false)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(() => typeof window !== 'undefined' && window.localStorage.getItem('oktapod.sidebar.collapsed') === '1')
  const [threadMenuTarget, setThreadMenuTarget] = useState<string>()
  const [renameTarget, setRenameTarget] = useState<string>()
  const [renameTitle, setRenameTitle] = useState('')
  const location = useRouterState({ select: (state) => state.location })
  const queryClient = useQueryClient()
  const projects = useQuery({ queryKey: ['projects'], queryFn: api.projects })
  const approvals = useQuery({ queryKey: ['approvals', 'pending'], queryFn: () => api.approvals('pending'), refetchInterval: 5_000 })
  const diagnostics = useQuery({ queryKey: ['diagnostics', 'shell'], queryFn: api.diagnostics, refetchInterval: 10_000, retry: false })
  const logout = useMutation({ mutationFn: api.logout, onSuccess: () => { window.sessionStorage.removeItem('oktapod.authenticated'); queryClient.clear(); window.location.replace('/login') } })
  const currentProject = projects.data?.items.find((project) => location.pathname.includes(project.id))
  const conversations = useQuery({ queryKey: ['conversations', currentProject?.id], queryFn: () => api.conversations(currentProject!.id), enabled: Boolean(currentProject?.id), refetchInterval: 2_000 })
  const projectTasks = useQuery({ queryKey: ['tasks', currentProject?.id], queryFn: () => api.tasks(currentProject!.id), enabled: Boolean(currentProject?.id), refetchInterval: 2_000 })
  const threadMatch = location.pathname.match(/\/threads\/([^/]+)/)
  const queryConversation = new URLSearchParams(location.search).get('conversation') ?? undefined
  const activeThreadId = threadMatch?.[1] ?? queryConversation ?? conversations.data?.items[0]?.id
  const deleteConversation = useMutation({
    mutationFn: (conversationId: string) => api.deleteConversation(conversationId),
    onSuccess: async (_, conversationId) => {
      setThreadMenuTarget(undefined)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['conversations', currentProject?.id] }),
        queryClient.invalidateQueries({ queryKey: ['tasks', currentProject?.id] }),
      ])
      if (conversationId === activeThreadId && currentProject) {
        window.location.assign(`/projects/${currentProject.id}?conversation=new`)
      }
    },
  })
  const renameConversation = useMutation({
    mutationFn: ({ conversationId, title }: { conversationId: string; title: string }) => api.renameConversation(conversationId, title),
    onSuccess: async () => {
      setRenameTarget(undefined)
      setThreadMenuTarget(undefined)
      await queryClient.invalidateQueries({ queryKey: ['conversations', currentProject?.id] })
    },
  })
  const closeMobile = () => setMobileOpen(false)
  const toggleSidebar = () => setSidebarCollapsed((value) => {
    const next = !value
    window.localStorage.setItem('oktapod.sidebar.collapsed', next ? '1' : '0')
    return next
  })

  return <div className={`codex-shell ${sidebarCollapsed && !mobileOpen ? 'sidebar-collapsed' : ''}`}>
    <nav className="global-rail" aria-label="全局导航">
      <Link className="rail-brand" to="/" title="Oktapod"><span>O</span></Link>
      <div className="rail-primary">
        <RailLink to="/" label="首页" active={location.pathname === '/'} icon={<Home />} />
        <RailLink to={currentProject ? '/projects/$projectId' : '/'} params={currentProject ? { projectId: currentProject.id } : undefined} label="项目" active={location.pathname.startsWith('/projects/')} icon={<Folder />} />
        <RailLink to="/resources" label="资源" active={location.pathname === '/resources'} icon={<PackageOpen />} />
        <RailLink to="/approvals" label="审批" active={location.pathname === '/approvals'} icon={<ShieldCheck />} badge={approvals.data?.items.length} />
        <RailLink to="/settings/providers" label="设置" active={location.pathname.startsWith('/settings')} icon={<Settings />} />
        {sidebarCollapsed ? <button className="rail-item" type="button" title="展开项目栏" aria-label="展开项目栏" onClick={toggleSidebar}><PanelLeftOpen /></button> : null}
      </div>
      <div className="rail-bottom">
        <Link className={`rail-item ${location.pathname === '/diagnostics' ? 'active' : ''}`} to="/diagnostics" title="诊断"><span className={`service-dot ${diagnostics.data?.status ?? 'unknown'}`} /><Activity /></Link>
        <button className="rail-avatar" type="button" title={`${bootstrap.user.display_name} · 退出登录`} disabled={logout.isPending} onClick={() => logout.mutate()}>{bootstrap.user.display_name.slice(0, 1).toUpperCase()}</button>
      </div>
    </nav>

    {mobileOpen ? <button className="sidebar-scrim" aria-label="关闭导航" type="button" onClick={closeMobile} /> : null}
    <aside className={`project-sidebar ${mobileOpen ? 'open' : ''}`}>
      <header className="project-sidebar-header">
        <button className="project-switcher" type="button"><span className="project-icon"><Folder size={15} /></span><span><strong>{currentProject?.name || '个人工作空间'}</strong><small>{currentProject ? 'Owner-only 私有项目' : `${projects.data?.items.length ?? 0} 个项目`}</small></span><ChevronDown size={14} /></button>
        <button className="icon-control desktop-collapse" type="button" aria-label={sidebarCollapsed ? '展开项目栏' : '收起项目栏'} onClick={toggleSidebar}>{sidebarCollapsed ? <PanelLeftOpen size={17} /> : <PanelLeftClose size={17} />}</button>
        <button className="icon-control mobile-close" type="button" aria-label="关闭导航" onClick={closeMobile}><X size={17} /></button>
      </header>

      {currentProject ? <>
        <Link className="new-thread-button" to="/projects/$projectId" params={{ projectId: currentProject.id }} search={{ conversation: 'new' }} onClick={closeMobile}><Plus size={15} /><span>新建 Thread</span><kbd>⌘ N</kbd></Link>
        <label className="thread-search"><Search size={14} /><input aria-label="搜索 Thread" placeholder="搜索 Thread" /></label>
        <div className="sidebar-label"><span>Threads</span><em>{conversations.data?.items.length ?? 0}</em></div>
        <div className="thread-nav">
          {conversations.data?.items.map((thread) => {
            const latestTask = projectTasks.data?.items.find((task) => task.conversation_id === thread.id)
            const menuOpen = threadMenuTarget === thread.id
            const renaming = renameTarget === thread.id
            return <div className={`thread-nav-row${thread.id === activeThreadId ? ' active' : ''}`} key={thread.id}>
              <Link to="/projects/$projectId/threads/$threadId" params={{ projectId: currentProject.id, threadId: thread.id }} onClick={closeMobile}><MessageSquare size={14} /><span><strong>{thread.title || '未命名 Thread'}</strong><small>{thread.last_message ? assistantExcerpt(thread.last_message, 90) : '尚无消息'}</small></span>{latestTask ? <ThreadStatusIcon status={latestTask.status} /> : null}</Link>
              <button className="thread-more" type="button" aria-label={`管理 ${thread.title || '未命名 Thread'}`} title="会话操作" aria-expanded={menuOpen} onClick={() => { deleteConversation.reset(); renameConversation.reset(); setRenameTarget(undefined); setThreadMenuTarget((value) => value === thread.id ? undefined : thread.id) }}><MoreHorizontal size={14} /></button>
              {menuOpen ? <div className="thread-action-menu">
                {renaming ? <form onSubmit={(event) => { event.preventDefault(); const title = renameTitle.trim(); if (title) renameConversation.mutate({ conversationId: thread.id, title }) }}>
                  <input autoFocus aria-label="Thread 名称" value={renameTitle} maxLength={200} onChange={(event) => setRenameTitle(event.target.value)} />
                  <button type="submit" aria-label="保存名称" disabled={!renameTitle.trim() || renameConversation.isPending}><Check size={13} /></button>
                </form> : <>
                  <button type="button" onClick={() => { setRenameTarget(thread.id); setRenameTitle(thread.title || '') }}><Pencil size={13} />编辑名称</button>
                  <button className="danger" type="button" disabled={deleteConversation.isPending} onClick={() => deleteConversation.mutate(thread.id)}><Trash2 size={13} />{deleteConversation.isPending ? '删除中' : '删除'}</button>
                </>}
              </div> : null}
            </div>
          })}
          {deleteConversation.error || renameConversation.error ? <p className="thread-delete-error" role="alert">{deleteConversation.error?.message || renameConversation.error?.message}</p> : null}
          {!conversations.isLoading && conversations.data?.items.length === 0 ? <p className="thread-nav-empty">发送第一条消息后，Thread 会出现在这里。</p> : null}
        </div>
        <nav className="project-tools" aria-label="项目工具">
          <Link to="/projects/$projectId" params={{ projectId: currentProject.id }} search={{ inspector: 'assets' }}><Folder size={15} />Files</Link>
          <Link to="/projects/$projectId" params={{ projectId: currentProject.id }} search={{ inspector: 'changes' }}><GitCompareArrows size={15} />Changes</Link>
          <Link to="/projects/$projectId/tree" params={{ projectId: currentProject.id }} search={{ thread: activeThreadId }}><Network size={15} />Session Tree</Link>
        </nav>
      </> : <>
        <Link className="new-thread-button" to="/"><Plus size={15} /><span>新建任务</span></Link>
        <div className="sidebar-label"><span>最近项目</span><em>{projects.data?.items.length ?? 0}</em></div>
        <div className="workspace-project-list">{projects.data?.items.map((project) => <Link key={project.id} to="/projects/$projectId" params={{ projectId: project.id }}><span className={`project-scene ${project.scene}`}><Folder size={14} /></span><span><strong>{project.name}</strong><small>{project.description || '个人私有项目'}</small></span></Link>)}</div>
        <nav className="project-tools global-tools"><Link to="/import-export"><FileUp size={15} />Import / Export</Link></nav>
      </>}
      <footer className="project-sidebar-footer"><span className="account-avatar">{bootstrap.user.display_name.slice(0, 1).toUpperCase()}</span><span><strong>{bootstrap.user.display_name}</strong><small>{bootstrap.model_configured ? '模型服务已配置' : '需要配置模型服务'}</small></span><button type="button" title="退出登录" onClick={() => logout.mutate()}><LogOut size={15} /></button></footer>
    </aside>

    <section className="codex-main">
      <header className="mobile-topbar"><button type="button" aria-label="打开导航" onClick={() => setMobileOpen(true)}><Menu size={18} /></button><strong>{currentProject?.name || 'Oktapod'}</strong><span className={`service-dot ${diagnostics.data?.status ?? 'unknown'}`} /></header>
      <div className="codex-content"><BootstrapContext.Provider value={bootstrap}>{children}</BootstrapContext.Provider></div>
    </section>
  </div>
}

function RailLink({ to, params, label, active, icon, badge }: { to: string; params?: Record<string, string>; label: string; active: boolean; icon: ReactNode; badge?: number }) {
  return <Link className={`rail-item ${active ? 'active' : ''}`} to={to} params={params} title={label}>{icon}<span>{label}</span>{badge ? <em>{badge}</em> : null}</Link>
}

function ThreadStatusIcon({ status }: { status: string }) {
  const labels: Record<string, string> = {
    succeeded: '已完成', failed: '失败', blocked: '已阻塞', waiting_approval: '待审批',
    waiting_input: '待输入', paused: '已暂停', queued: '排队中', running: '运行中', canceled: '已取消',
  }
  const label = labels[status] ?? status
  return <span className={`thread-task-state ${status}`} aria-label={label} title={label}>
    {status === 'succeeded' ? <CheckCircle2 size={12} /> :
      status === 'failed' || status === 'blocked' ? <AlertTriangle size={12} /> :
        status === 'waiting_approval' || status === 'waiting_input' ? <ShieldAlert size={12} /> :
          status === 'paused' ? <CirclePause size={12} /> :
            status === 'running' || status === 'queued' ? <LoaderCircle className={status === 'running' ? 'spin' : ''} size={12} /> :
              <X size={12} />}
  </span>
}
