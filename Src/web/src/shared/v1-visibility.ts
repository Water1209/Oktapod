// V1 可见性是产品范围，而不是各页面的临时条件。后端和旧数据仍保留兼容，
// 但新的导航、路由和 Composer 入口必须从这里取得是否可见的结论。
export const V1_VISIBILITY = {
  teamProjects: false,
  agentTeams: false,
  automations: false,
  projectMemory: false,
  packages: false,
  standaloneTerminal: false,
  invitations: false,
} as const

export const V1_INSPECTOR_TABS = new Set([
  'files', 'changes', 'usage', 'overview', 'trace',
])

export function visibleInspectorTab(value: string | undefined) {
  if (value && V1_INSPECTOR_TABS.has(value)) return value
  if (value === 'project_usage') return 'usage'
  if (value && ['run', 'context', 'tools', 'security'].includes(value)) return 'trace'
  if (value && ['tasks', 'assets', 'activity', 'plan', 'agents'].includes(value)) return 'overview'
  return 'files'
}
