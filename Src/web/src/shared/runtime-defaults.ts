export const TASK_RUNTIME_DEFAULTS = {
  maxModelCalls: 80,
  maxTokens: 1_000_000,
  modelRetryLimit: 3,
  toolRetryLimit: 3,
  modelOutputTokens: [8_192, 16_384, 32_768],
  contextRetainTokens: 20_000,
} as const
