import type { RunUsage } from '../../shared/api/client'

export function recommendedRunBudget(usage: RunUsage) {
  const consumedCalls = usage.model_calls + usage.reserved_calls
  const consumedTokens = usage.input_tokens + usage.output_tokens + usage.reasoning_tokens + usage.reserved_tokens
  const callsExhausted = usage.budget_max_calls > 0 && usage.remaining_calls <= 0
  const tokensExhausted = usage.budget_max_tokens > 0 && usage.remaining_tokens <= 0
  return {
    maxCalls: callsExhausted
      ? Math.min(1_000, Math.max(usage.budget_max_calls + 5, Math.ceil(consumedCalls * 1.25)))
      : usage.budget_max_calls,
    maxTokens: tokensExhausted
      ? Math.min(10_000_000, Math.max(usage.budget_max_tokens + 50_000, Math.ceil(consumedTokens * 1.25 / 1_000) * 1_000))
      : usage.budget_max_tokens,
  }
}
