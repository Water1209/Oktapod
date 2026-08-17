import type { Capability, MCPDefinitionV1 } from '../../shared/api/client'

type StoredMCPDefinition = Partial<Omit<MCPDefinitionV1, 'transport' | 'auth'>> & {
  transport?: MCPDefinitionV1['transport'] | 'stdio' | 'streamable_http'
  auth?: MCPDefinitionV1['auth']
  auth_env?: string
}

export function mcpSecretBindings(item?: Pick<Capability, 'kind' | 'manifest_json'>) {
  if (!item || item.kind !== 'mcp') return []
  try {
    const definition = JSON.parse(item.manifest_json) as StoredMCPDefinition
    if (definition.secret_bindings?.length) return definition.secret_bindings
    const transportType = typeof definition.transport === 'string' ? definition.transport : definition.transport?.type
    const authEnv = typeof definition.transport === 'object' && 'auth_env' in definition.transport ? definition.transport.auth_env : definition.auth_env
    if (transportType === 'stdio' && authEnv) return [{ name: 'credential', target: `env:${authEnv}`, required: true }]
    if (definition.auth?.type === 'bearer') return [{ name: 'credential', target: 'header:Authorization', required: true }]
    if (definition.auth?.type === 'custom_header') return [{ name: 'credential', target: `header:${definition.auth.header || 'X-API-Key'}`, required: true }]
    return []
  } catch { return [] }
}
