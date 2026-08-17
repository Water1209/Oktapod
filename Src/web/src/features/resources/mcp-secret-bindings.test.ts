import { describe, expect, it } from 'vitest'
import { mcpSecretBindings } from './mcp-secret-bindings'

describe('mcpSecretBindings', () => {
  it('keeps legacy stdio auth_env resources repairable', () => {
    expect(mcpSecretBindings({ kind: 'mcp', manifest_json: JSON.stringify({ transport: 'stdio', command: 'server', auth_env: 'SERVICE_API_KEY' }) })).toEqual([
      { name: 'credential', target: 'env:SERVICE_API_KEY', required: true },
    ])
  })

  it('derives bearer credentials when explicit bindings are absent', () => {
    expect(mcpSecretBindings({ kind: 'mcp', manifest_json: JSON.stringify({ transport: { type: 'streamable_http', url: 'https://example.test/mcp' }, auth: { type: 'bearer' } }) })).toEqual([
      { name: 'credential', target: 'header:Authorization', required: true },
    ])
  })
})
