export const TASK_RUNTIME_DEFAULTS = {
  maxModelCalls: 32,
  maxTokens: 200_000,
  modelRetryLimit: 3,
  toolRetryLimit: 3,
  modelOutputTokens: [2_048, 4_096, 8_192],
  contextRetainTokens: 20_000,
} as const