import { describe, expect, it } from 'vitest'
import type { RunUsage } from '../../shared/api/client'
import { recommendedRunBudget } from './run-budget'

const usage: RunUsage = {
  run_id: 'run-1', model_calls: 10, input_tokens: 108_000, output_tokens: 4_144,
  cached_tokens: 0, cache_write_tokens: 0, reasoning_tokens: 0, estimated_calls: 0, latency_ms: 0,
  first_byte_ms: 0, first_safe_delta_ms: 0, assembly_ms: 0, request_bytes: 0, model_call_purposes: {},
  cost_status: 'unpriced', currency: 'USD', cost_decimal: '0',
  budget_max_calls: 20, budget_max_tokens: 100_000, budget_status: 'exhausted',
  remaining_calls: 10, remaining_tokens: 0, reserved_calls: 0, reserved_tokens: 0,
}

describe('run budget recovery', () => {
  it('only raises the exhausted token dimension and leaves call capacity unchanged', () => {
    expect(recommendedRunBudget(usage)).toEqual({ maxCalls: 20, maxTokens: 150_000 })
  })

  it('raises calls when that dimension is also exhausted', () => {
    expect(recommendedRunBudget({ ...usage, model_calls: 20, remaining_calls: 0 })).toEqual({ maxCalls: 25, maxTokens: 150_000 })
  })

  it('does not treat zero-valued unlimited dimensions as exhausted', () => {
    expect(recommendedRunBudget({ ...usage, budget_max_calls: 0, budget_max_tokens: 0, remaining_calls: 0, remaining_tokens: 0 })).toEqual({ maxCalls: 0, maxTokens: 0 })
  })
})
