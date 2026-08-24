import { describe, expect, it } from 'vitest'
import type { Capability } from '../../shared/api/client'
import { resourceProfile } from './resource-profile'

const item = (overrides: Partial<Capability>): Capability => ({
  id: 'resource-1',
  organization_id: 'org',
  project_id: 'project',
  owner_user_id: 'owner',
  kind: 'skill',
  name: 'release-check',
  version: '1.0.0',
  manifest_json: '{}',
  content_hash: 'sha256:resource',
  status: 'enabled',
  availability: 'available',
  risk_level: 'L0',
  permission_diff: [],
  scope_type: 'project',
  scope_id: 'project',
  source_filename: 'SKILL.md',
  bundle_hash: 'sha256:bundle',
  install_job_id: '',
  credential_configured: false,
  credential_state: 'ready',
  origin: 'custom',
  dependencies: [],
  update_state: 'current',
  created_at: '2026-08-24T00:00:00Z',
  updated_at: '2026-08-24T00:00:00Z',
  ...overrides,
})

describe('resource capability profile', () => {
  it('explains a Skill from its description, tools, and triggers', () => {
    const profile = resourceProfile(item({ manifest_json: JSON.stringify({
      description: '发布前完成测试与证据核对。',
      allowed_tools: ['git_status', 'run_command'],
      triggers: ['发布检查'],
    }) }))

    expect(profile.summary).toBe('发布前完成测试与证据核对。')
    expect(profile.abilities.map((ability) => ability.title)).toEqual(['git_status', 'run_command'])
    expect(profile.useCases).toEqual(['对话提到“发布检查”时按需调用'])
  })

  it('uses connected MCP tool descriptions as the detailed capability list', () => {
    const profile = resourceProfile(item({ kind: 'mcp', name: '@playwright/mcp', manifest_json: JSON.stringify({
      schema_version: 'oktapod.mcp/v1',
      description: '在真实浏览器中验证页面。',
    }) }), JSON.stringify([
      { name: 'browser_navigate', description: 'Navigate to a URL.' },
      { name: 'browser_snapshot', description: 'Read the accessibility snapshot.' },
    ]))

    expect(profile.summary).toBe('在真实浏览器中验证页面。')
    expect(profile.abilities).toEqual([
      { title: 'browser_navigate', detail: 'Navigate to a URL.' },
      { title: 'browser_snapshot', detail: 'Read the accessibility snapshot.' },
    ])
  })

  it('gives built-in tools a concrete purpose instead of generic metadata', () => {
    const profile = resourceProfile(item({ kind: 'plugin', name: 'grep', origin: 'system' }))
    expect(profile.summary).toContain('UTF-8')
    expect(profile.useCases).toContain('追踪符号引用')
  })
})
