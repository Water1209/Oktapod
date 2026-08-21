import type { WebAccessConfiguration } from '../../shared/api/client'

type PrivateFetchDraft = WebAccessConfiguration['private_fetch'] | undefined

// This is an authoring aid, not the security boundary. The Go configuration
// validator repeats the complete address-class and listener-origin checks.
export function validatePrivateFetchDraft(privateFetch: PrivateFetchDraft): string {
  if (!privateFetch || !privateFetch.enabled || privateFetch.origins_redacted) return ''
  if (privateFetch.allowed_origins.length === 0) return '启用私网读取前至少添加一个有效 origin。'

  const ids = new Set<string>()
  const origins = new Set<string>()
  for (const [index, item] of privateFetch.allowed_origins.entries()) {
    const label = `规则 ${index + 1}`
    const id = item.id.trim()
    if (!/^[a-z0-9][a-z0-9_-]{0,63}$/.test(id)) {
      return `${label} 的 ID 必须是 1–64 位小写字母、数字、连字符或下划线。`
    }
    if (ids.has(id)) return `${label} 的 ID 与已有规则重复。`
    ids.add(id)

    const origin = item.origin.trim()
    const match = /^(https?):\/\/(\[[0-9a-f:.]+\]|(?:\d{1,3}\.){3}\d{1,3}):([0-9]{1,5})$/i.exec(origin)
    if (!match) return `${label} 必须是 HTTP/HTTPS、字面量 IP 和显式端口组成的 origin，不能包含域名或路径。`
    const host = match[2] ?? ''
    const port = Number(match[3] ?? '0')
    if (port < 1 || port > 65_535) return `${label} 的端口必须在 1–65535 之间。`
    if (!host.startsWith('[') && host.split('.').some((part) => Number(part) > 255)) {
      return `${label} 包含无效的 IPv4 地址。`
    }
    try {
      const parsed = new URL(origin)
      if (parsed.username || parsed.password || parsed.pathname !== '/' || parsed.search || parsed.hash) throw new Error('not an origin')
      const canonical = `${parsed.protocol}//${parsed.host}`.toLowerCase()
      if (origins.has(canonical)) return `${label} 的 origin 与已有规则重复。`
      origins.add(canonical)
    } catch {
      return `${label} 包含无效的 origin。`
    }
  }
  return ''
}
