import { lazy, Suspense, type ComponentType } from 'react'
import { createRootRoute, createRoute, createRouter, Link, Navigate } from '@tanstack/react-router'
import { AuthenticatedRoot } from './authenticated-root'

const ProjectsPage = lazyNamed(() => import('../features/projects/projects-page'), 'ProjectsPage')
const ProjectLibraryPage = lazyNamed(() => import('../features/projects/projects-page'), 'ProjectLibraryPage')
const ProjectPage = lazyNamed(() => import('../features/projects/project-page'), 'ProjectPage')
const LegacyTaskResolver = lazyNamed(() => import('../features/tasks/legacy-task-resolver'), 'LegacyTaskResolver')
const ApprovalInbox = lazyNamed(() => import('../features/approvals/approval-inbox'), 'ApprovalInbox')
const ProviderSettingsPage = lazyNamed(() => import('../features/providers/provider-settings-page'), 'ProviderSettingsPage')
const ResourcesPage = lazyNamed(() => import('../features/resources/resources-page'), 'ResourcesPage')
const DiagnosticsPage = lazyNamed(() => import('../features/diagnostics/diagnostics-page'), 'DiagnosticsPage')
const SessionTreePage = lazyNamed(() => import('../features/sessions/session-tree-page'), 'SessionTreePage')
const ImportExportPage = lazyNamed(() => import('../features/migration/import-export-page'), 'ImportExportPage')
const SettingsPage = lazyNamed(() => import('../features/settings/settings-page'), 'SettingsPage')

type ProjectSearch = {
  conversation?: string
  inspector?: string
  event?: string
}

const rootRoute = createRootRoute({
  component: AuthenticatedRoot,
  notFoundComponent: () => (
    <div className="empty-panel">
      <p className="eyebrow">404</p>
      <h1>这个工作面不存在</h1>
      <p>返回项目首页，继续当前任务。</p>
      <Link className="primary-button" to="/">返回项目首页</Link>
    </div>
  ),
})

const projectsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/',
  component: suspend(ProjectsPage),
})

const projectLibraryRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/projects',
  component: suspend(ProjectLibraryPage),
})

const projectRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/projects/$projectId',
  validateSearch: projectSearch,
  component: suspend(ProjectPage),
})

const threadRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/projects/$projectId/threads/$threadId',
  validateSearch: projectSearch,
  component: suspend(ProjectPage),
})

const sessionTreeRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/projects/$projectId/tree',
  validateSearch: (search: Record<string, unknown>) => typeof search.thread === 'string' ? { thread: search.thread } : {},
  component: suspend(SessionTreePage),
})

// 保留旧路径的类型兼容和已有链接可解析性，但 V1 不再暴露独立 Terminal。
const terminalRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/projects/$projectId/terminal',
  component: () => <Navigate to="/" />,
})

const taskRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/tasks/$taskId',
  component: suspend(LegacyTaskResolver),
})

const approvalsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/approvals',
  component: suspend(ApprovalInbox),
})

const resourcesRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/resources',
  component: suspend(ResourcesPage),
})

const providersRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/settings/providers',
  component: suspend(ProviderSettingsPage),
})

const settingsIndexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/settings',
  component: () => <Navigate to="/settings/$section" params={{ section: 'appearance' }} />,
})

const legacySecuritySettingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/settings/security',
  component: () => <Navigate to="/approvals" />,
})

const legacyDataSettingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/settings/data',
  component: () => <Navigate to="/import-export" />,
})

const settingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/settings/$section',
  component: suspend(SettingsPage),
})

const importExportRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/import-export',
  component: suspend(ImportExportPage),
})

const diagnosticsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/diagnostics',
  component: suspend(DiagnosticsPage),
})

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  component: () => <Navigate to="/" />,
})

// 邀请是团队能力；V1 保留后端兼容，但不让页面成为可访问入口。
const invitationAcceptRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/invitations/accept',
  validateSearch: (search: Record<string, unknown>) => ({ token: typeof search.token === 'string' ? search.token : '' }),
  component: () => <Navigate to="/" />,
})

const routeTree = rootRoute.addChildren([projectsRoute, projectLibraryRoute, projectRoute, threadRoute, sessionTreeRoute, terminalRoute, taskRoute, approvalsRoute, resourcesRoute, providersRoute, settingsIndexRoute, legacySecuritySettingsRoute, legacyDataSettingsRoute, settingsRoute, importExportRoute, diagnosticsRoute, loginRoute, invitationAcceptRoute])

export const router = createRouter({ routeTree })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}

function projectSearch(search: Record<string, unknown>): ProjectSearch {
  return {
    ...(typeof search.conversation === 'string' ? { conversation: search.conversation } : {}),
    ...(typeof search.inspector === 'string' ? { inspector: search.inspector } : {}),
    ...(typeof search.event === 'string' ? { event: search.event } : {}),
  }
}

function lazyNamed<T extends Record<string, unknown>, K extends keyof T>(loader: () => Promise<T>, name: K) {
  return lazy(async () => ({ default: (await loader())[name] as ComponentType }))
}

function suspend(Page: ComponentType) {
  return function SuspendedRoute() {
    return <Suspense fallback={<div className="page-loading">正在打开工作面…</div>}><Page /></Suspense>
  }
}
