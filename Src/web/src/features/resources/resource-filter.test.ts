import { describe, expect, it } from 'vitest'
import type { Capability } from '../../shared/api/client'
import { filterProjectResources, filterSystemResources, resourceEmptyCopy, resourceKindCounts } from './resource-filter'

const item = (id: string, kind: Capability['kind']): Capability => ({
  id,
  organization_id: 'org',
  project_id: 'project',
  owner_user_id: 'owner',
  kind,
  name: id,
  version: '1',
  manifest_json: '{}',
  content_hash: `hash:${id}`,
  status: 'enabled',
  availability: 'available',
  risk_level: 'L0',
  scope_type: 'project',
  scope_id: 'project',
  source_filename: 'project',
  bundle_hash: `bundle:${id}`,
  install_job_id: '',
  permission_diff: [],
  credential_state: 'ready',
  credential_configured: true,
  origin: 'custom',
  dependencies: [],
  update_state: 'current',
  created_at: '2026-08-13T00:00:00Z',
  updated_at: '2026-08-13T00:00:00Z',
})

describe('resource scope and kind state', () => {
  const project = [item('skill-a', 'skill'), item('mcp-a', 'mcp'), item('agent-a', 'agent')]
  const system = [item('builtin-a', 'plugin')]

  it('keeps project totals stable while filtering by kind', () => {
    expect(resourceKindCounts(project)).toEqual({ all: 3, skill: 1, mcp: 1, agent: 1 })
    expect(filterProjectResources(project, 'mcp', '')).toHaveLength(1)
    expect(resourceKindCounts(project).all).toBe(3)
  })

  it('does not apply project kind filters to system resources', () => {
    expect(filterSystemResources(system, '')).toEqual(system)
    expect(filterSystemResources(system, 'missing')).toEqual([])
  })

  it('explains an empty MCP scope instead of showing an ambiguous blank state', () => {
    expect(resourceEmptyCopy('project', 'mcp', false)).toEqual({
      title: '当前项目没有 MCP',
      detail: '添加 MCP，或切换项目查看其他项目的资源。',
    })
  })
})
