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
    expect(profile.abilities.map((ability) => ability.title)).toEqual(['专业工作流', '稳定交付'])
    expect(profile.useCases).toEqual(['发布检查'])
    expect(profile.usage[0]).toContain('release-check')
  })

  it('uses connected MCP tool descriptions as the detailed capability list', () => {
    const profile = resourceProfile(item({ kind: 'mcp', name: 'custom-browser-mcp', manifest_json: JSON.stringify({
      schema_version: 'oktapod.mcp/v1',
      description: '在真实浏览器中验证页面。',
    }) }), JSON.stringify([
      { name: 'browser_navigate', description: 'Navigate to a URL.' },
      { name: 'browser_snapshot', description: 'Read the accessibility snapshot.' },
    ]))

    expect(profile.summary).toBe('在真实浏览器中验证页面。')
    expect(profile.abilities).toEqual([
      { title: '打开网页', technicalName: 'browser_navigate', detail: '打开指定网页地址，并返回页面加载后的状态。' },
      { title: '读取页面结构', technicalName: 'browser_snapshot', detail: '读取当前页面的结构与内容，供后续判断和操作。' },
    ])
    expect(profile.useCases).toContain('访问目标页面并读取结构、文本、链接或当前状态')
  })

  it('describes Playwright and frontend-design by their actual user capabilities', () => {
    const playwright = resourceProfile(item({ kind: 'mcp', name: '@playwright/mcp', source_filename: '@playwright/mcp' }), JSON.stringify([
      { name: 'browser_navigate', description: 'Navigate to a URL.' },
      { name: 'browser_snapshot', description: 'Capture an accessibility snapshot.' },
      { name: 'browser_click', description: 'Click a page element.' },
    ]))
    expect(playwright.displayName).toBe('Playwright MCP')
    expect(playwright.summary).toContain('真实浏览器')
    expect(playwright.abilities.map((ability) => ability.title)).toEqual(['打开网页', '读取页面结构', '点击元素'])
    expect(playwright.abilities.map((ability) => ability.detail)).toContain('读取当前页面的结构与内容，供后续判断和操作。')
    expect(playwright.useCases).toEqual([
      '访问目标页面并读取结构、文本、链接或当前状态',
      '自动执行点击、输入、表单、键盘或文件交互',
    ])

    const frontend = resourceProfile(item({ name: 'frontend-design', manifest_json: JSON.stringify({
      description: 'Guidance for distinctive, intentional visual design when building new UI or reshaping an existing one.',
      instructions: '# Frontend Design\n\n## Ground it in the subject\nChoose one concrete subject and audience.\n\n## Design principles\nTypography carries the personality of the page.\n\n## Process\nBrainstorm, critique, build, and critique again.',
    }) }))
    expect(frontend.displayName).toBe('Frontend Design')
    expect(frontend.abilities.map((ability) => ability.title)).toEqual(['专业工作流', '稳定交付'])
    expect(frontend.abilities[1]?.detail).toContain('Skill 自身说明')
    expect(frontend.summary).not.toContain('触发')
    expect(frontend.summary).toContain('专业工作方法')
    expect(frontend.useCases.every((useCase) => /[\u3400-\u9fff]/.test(useCase))).toBe(true)
  })

  it('presents externally discovered Tavily descriptions in Chinese', () => {
    const profile = resourceProfile(item({ kind: 'mcp', name: 'Tavily', description: 'Tavily official MCP server for web search, extract, map, and crawl.' } as Partial<Capability>), JSON.stringify([
      { name: 'tavily_search', description: 'Search the web for current information on any topic.' },
      { name: 'tavily_extract', description: 'Extract content from URLs.' },
      { name: 'tavily_crawl', description: 'Crawl a website starting from a URL.' },
      { name: 'tavily_map', description: 'Map a website structure.' },
      { name: 'tavily_research', description: 'Perform comprehensive research.' },
    ]))

    expect(profile.summary).toContain('联网检索')
    expect(profile.abilities.map((ability) => ability.title)).toEqual(['联网搜索', '网页内容提取', '网站遍历', '网站结构梳理', '专题研究'])
    expect(profile.abilities.every((ability) => /[\u3400-\u9fff]/.test(ability.detail))).toBe(true)
  })

  it('gives built-in tools a concrete purpose instead of generic metadata', () => {
    const profile = resourceProfile(item({ kind: 'plugin', name: 'grep', origin: 'system' }))
    expect(profile.summary).toContain('UTF-8')
    expect(profile.useCases).toContain('追踪符号引用')
  })
})
