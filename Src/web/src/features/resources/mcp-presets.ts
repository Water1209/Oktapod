import type { CapabilityScope } from '../../shared/api/client'

export type ResourceInstallPreset = {
  type: 'skill' | 'mcp' | 'agent'
  name: string
  version: string
  manifest: string
  input?: string
  credential?: string
  scope?: CapabilityScope
}

export const TAVILY_MCP_PRESET: ResourceInstallPreset = {
  type: 'mcp',
  name: 'tavily',
  version: 'remote-2026-08',
  scope: 'global',
  input: 'Tavily',
  manifest: JSON.stringify({
    schema_version: 'oktapod.mcp/v1',
    source: { type: 'official', reference: 'https://github.com/tavily-ai/tavily-mcp', publisher: 'Tavily' },
    transport: { type: 'streamable_http', url: 'https://mcp.tavily.com/mcp/' },
    auth: { type: 'bearer' },
    secret_bindings: [{ name: 'credential', target: 'header:Authorization', required: true }],
    permissions: ['network'],
    description: 'Tavily official MCP server for web search, extract, map, and crawl.',
  }, null, 2),
}

export function mcpPresetFromInput(input: string): ResourceInstallPreset {
  const value = input.trim()
  if (!value || /^(tavily|tavily mcp)$/i.test(value)) return TAVILY_MCP_PRESET
  if (/^https?:\/\//i.test(value)) {
    const url = new URL(value)
    return {
      type: 'mcp', name: url.hostname.split('.')[0] || 'remote-mcp', version: 'remote', scope: 'project', input: value,
      manifest: JSON.stringify({ schema_version: 'oktapod.mcp/v1', source: { type: 'url', reference: value }, transport: { type: 'streamable_http', url: value }, auth: { type: 'none' }, permissions: ['network'] }, null, 2),
    }
  }
  const words = value.match(/(?:"[^"]+"|'[^']+'|\S+)/g)?.map((word) => word.replace(/^['"]|['"]$/g, '')) ?? []
  const command = words[0] || 'npx'
  const args = words.slice(1)
  const packageReference = command === 'npx' || command === 'uvx' ? args.find((arg) => !arg.startsWith('-')) ?? value : value
  const version = packageReference.match(/@([^@/]+)$/)?.[1] ?? 'local'
  const unversionedReference = version === 'local' ? packageReference : packageReference.slice(0, -(version.length + 1))
  const displayName = unversionedReference.split('/').filter(Boolean).at(-1) ?? unversionedReference
  return {
    type: 'mcp', name: displayName.replace(/^@/, '').replace(/[^a-z0-9-]+/gi, '-').replace(/^-|-$/g, '') || 'local-mcp', version, scope: 'project', input: value,
    manifest: JSON.stringify({ schema_version: 'oktapod.mcp/v1', source: { type: command === 'npx' ? 'npm' : command === 'uvx' ? 'pypi' : 'command', reference: packageReference }, transport: { type: 'stdio', command, args }, auth: { type: 'none' }, permissions: ['process'] }, null, 2),
  }
}
