import type { Capability } from '../../shared/api/client'

export type ResourceView = 'project' | 'system'
export type ResourceKind = 'all' | 'skill' | 'mcp' | 'agent'

export function resourceKindCounts(items: Capability[]) {
  return {
    all: items.length,
    skill: items.filter((item) => item.kind === 'skill').length,
    mcp: items.filter((item) => item.kind === 'mcp').length,
    agent: items.filter((item) => item.kind === 'agent').length,
  } satisfies Record<ResourceKind, number>
}

export function filterProjectResources(items: Capability[], kind: ResourceKind, query: string) {
  const needle = query.trim().toLowerCase()
  return items.filter((item) => (kind === 'all' || item.kind === kind) && (!needle || `${item.name} ${item.source_filename}`.toLowerCase().includes(needle)))
}

export function filterSystemResources(items: Capability[], query: string) {
  const needle = query.trim().toLowerCase()
  return items.filter((item) => !needle || `${item.name} ${item.source_filename}`.toLowerCase().includes(needle))
}

export function resourceEmptyCopy(view: ResourceView, kind: ResourceKind, hasQuery: boolean) {
  if (hasQuery) return { title: '没有搜索结果', detail: '清除搜索词或切换资源范围。' }
  if (view === 'system') return { title: '没有系统能力', detail: '当前版本没有可展示的系统能力。' }
  if (kind === 'mcp') return { title: '当前项目没有 MCP', detail: '添加 MCP，或切换项目查看其他项目的资源。' }
  if (kind === 'skill') return { title: '当前项目没有 Skill', detail: '通过对话创建或导入一个 Skill。' }
  if (kind === 'agent') return { title: '当前项目没有 Agent', detail: '在项目内创建 Agent 定义后会显示在这里。' }
  return { title: '当前项目没有资源', detail: '使用“添加资源”安装 Skill 或 MCP。' }
}
