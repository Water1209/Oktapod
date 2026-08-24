import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiProblem, api } from './client'

describe('API client', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('decodes bootstrap state without persisting a credential', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      profile: 'personal',
      model_endpoint: 'https://provider.example/v1',
      model_configured: true,
      user: { id: 'user-id', display_name: '本地用户' },
      organization: { id: 'org-id', name: '个人空间' },
      capabilities: {
        os: 'windows',
        sandbox: 'restricted-native',
        filesystem: 'workspace-scoped',
        command: 'structured-no-shell',
        network: 'search-only-fixed-provider',
        browser: 'not-configured',
        mcp: 'approval-gated',
      },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    const state = await api.bootstrap()

    expect(state.model_configured).toBe(true)
    expect(state).not.toHaveProperty('model_api_key')
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/bootstrap', expect.objectContaining({ credentials: 'same-origin' }))
  })

  it('surfaces stable problem details', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      type: 'about:blank',
      title: 'Invalid request',
      status: 400,
      detail: 'goal is required',
      code: 'VALIDATION_FAILED',
      correlation_id: 'correlation-test',
    }), { status: 400, headers: { 'Content-Type': 'application/problem+json' } })))

    try {
      await api.projects()
      throw new Error('expected request to fail')
    } catch (error: unknown) {
      expect(error).toBeInstanceOf(ApiProblem)
      if (!(error instanceof ApiProblem)) throw error
      expect(error.problem).toMatchObject({ code: 'VALIDATION_FAILED', correlation_id: 'correlation-test' })
    }
  })

  it('retries a connection failure once with the same idempotency key', async () => {
    const agent = {
      id: 'agent-id', organization_id: 'org-id', project_id: 'project-id', owner_user_id: 'user-id',
      name: 'Reviewer', version: '1.0.0', instructions: 'Review evidence.', model: 'model-e2e', fallback_model: '',
      config_json: '{}', status: 'enabled', created_at: '2026-08-24T00:00:00Z', updated_at: '2026-08-24T00:00:00Z',
    }
    const fetchMock = vi.fn()
      .mockRejectedValueOnce(new TypeError('Failed to fetch'))
      .mockResolvedValueOnce(new Response(JSON.stringify(agent), { status: 201, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(api.createAgent('project-id', { name: 'Reviewer', version: '1.0.0', instructions: 'Review evidence.' })).resolves.toMatchObject({ id: 'agent-id' })

    expect(fetchMock).toHaveBeenCalledTimes(2)
    const firstHeaders = new Headers(fetchMock.mock.calls[0]?.[1]?.headers)
    const secondHeaders = new Headers(fetchMock.mock.calls[1]?.[1]?.headers)
    expect(firstHeaders.get('Idempotency-Key')).toBeTruthy()
    expect(secondHeaders.get('Idempotency-Key')).toBe(firstHeaders.get('Idempotency-Key'))
  })

  it('does not retry a read without an idempotency key', async () => {
    const fetchMock = vi.fn().mockRejectedValue(new TypeError('Failed to fetch'))
    vi.stubGlobal('fetch', fetchMock)

    await expect(api.projects()).rejects.toThrow('Failed to fetch')
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
