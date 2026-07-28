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
})
