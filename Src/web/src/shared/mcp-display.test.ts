import { describe, expect, it } from 'vitest'
import { mcpToolDisplayLabel, mcpToolStepLabel } from './mcp-display'

describe('MCP tool display', () => {
  it('uses the immutable capability name instead of exposing the dynamic tool identity', () => {
    expect(mcpToolDisplayLabel('mcp__capability-id__browser_navigate', '@playwright/mcp · browser_navigate')).toBe('Playwright MCP · 打开网页')
    expect(mcpToolStepLabel('mcp__capability-id__browser_navigate', '@playwright/mcp · browser_navigate')).toBe('Playwright MCP 打开网页')
  })

  it('does not guess that every browser MCP is Playwright', () => {
    expect(mcpToolDisplayLabel('mcp__legacy-id__browser_navigate')).toBe('浏览器 MCP · 打开网页')
  })
})
