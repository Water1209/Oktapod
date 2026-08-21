import { describe, expect, it } from 'vitest'
import type { WebAccessConfiguration } from '../../shared/api/client'
import { validatePrivateFetchDraft } from './private-fetch-validation'

function draft(overrides: Partial<WebAccessConfiguration['private_fetch']> = {}): WebAccessConfiguration['private_fetch'] {
  return { enabled: true, allowed_origins: [], allowed_origin_count: 0, origins_redacted: false, ...overrides }
}

describe('validatePrivateFetchDraft', () => {
  it('requires a complete rule before save', () => {
    expect(validatePrivateFetchDraft(draft())).toContain('至少添加')
    expect(validatePrivateFetchDraft(draft({ allowed_origins: [{ id: 'wiki', origin: '' }] }))).toContain('字面量 IP')
  })

  it('accepts exact IPv4 and bracketed IPv6 origins', () => {
    expect(validatePrivateFetchDraft(draft({ allowed_origins: [{ id: 'wiki', origin: 'http://192.168.10.20:8080' }] }))).toBe('')
    expect(validatePrivateFetchDraft(draft({ allowed_origins: [{ id: 'ipv6-service', origin: 'https://[fd00::20]:8443' }] }))).toBe('')
  })

  it('rejects domains, paths, invalid ports, and duplicates', () => {
    expect(validatePrivateFetchDraft(draft({ allowed_origins: [{ id: 'wiki', origin: 'http://wiki.internal:8080' }] }))).toContain('字面量 IP')
    expect(validatePrivateFetchDraft(draft({ allowed_origins: [{ id: 'wiki', origin: 'http://127.0.0.1:3000/docs' }] }))).toContain('路径')
    expect(validatePrivateFetchDraft(draft({ allowed_origins: [{ id: 'wiki', origin: 'http://127.0.0.1:99999' }] }))).toContain('端口')
    expect(validatePrivateFetchDraft(draft({ allowed_origins: [
      { id: 'wiki', origin: 'http://127.0.0.1:3000' },
      { id: 'wiki', origin: 'http://127.0.0.1:3001' },
    ] }))).toContain('ID')
  })

  it('does not expose or block redacted member configuration', () => {
    expect(validatePrivateFetchDraft(draft({ enabled: true, origins_redacted: true }))).toBe('')
    expect(validatePrivateFetchDraft(draft({ enabled: false }))).toBe('')
  })
})
