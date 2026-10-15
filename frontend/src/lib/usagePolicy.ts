import type { AccountUsagePolicy } from '../api/usage'

export type ModelTokenLimit = { model: string; tokens: number }

function modelKey(model: string) {
  return model.trim().toLowerCase().replace(/^(qoder|workbuddy|trae|devin|command|codex|zhipu)\//, '').replace(/[_ ]/g, '-')
}

export function validateUsagePolicy(policy: AccountUsagePolicy, models: ModelTokenLimit[]) {
  if ([policy.reserve_credits, policy.daily_credits].some((value) => !Number.isFinite(value) || value < 0)) return 'usagePolicyInvalid'
  if (!Number.isSafeInteger(policy.daily_tokens) || policy.daily_tokens < 0) return 'usagePolicyInvalid'
  const seen = new Set<string>()
  for (const row of models) {
    const model = modelKey(row.model)
    if (!model || seen.has(model) || !Number.isSafeInteger(row.tokens) || row.tokens <= 0) return 'usageModelInvalid'
    seen.add(model)
  }
  return ''
}

export function usagePolicyWithModels(policy: AccountUsagePolicy, models: ModelTokenLimit[]): AccountUsagePolicy {
  return { ...policy, model_daily_tokens: Object.fromEntries(models.map((row) => [modelKey(row.model), row.tokens])) }
}
