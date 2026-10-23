import type { AccountUsagePolicy } from '../api/usage'

export type ModelTokenLimit = { model: string; tokens: number }

export function canonicalUsageModelKey(model: string) {
  return model.trim().toLowerCase().replace(/^(qoder|workbuddy|trae|devin|command|codex|zhipu)\//, '').replace(/[_ ]/g, '-')
}

export function hasModelUsageLimit(policy: AccountUsagePolicy, model: string) {
  return Object.prototype.hasOwnProperty.call(policy.model_daily_tokens || {}, canonicalUsageModelKey(model))
}

export function safeBudgetGuardSuggestion(budget: {
  estimated_tokens: number | null
  complete_windows: number
  observation_truncated: boolean
  basis: string
  reference?: { estimated_tokens: number | null; sample_count: number; account_count: number; confidence: string; scope: string }
}) {
  if (budget.observation_truncated) return null
  const ownIsRepeated = budget.basis === 'explicit_6004_reset_windows_weighted_recent' && budget.complete_windows >= 2
  if (ownIsRepeated && Number.isSafeInteger(budget.estimated_tokens) && (budget.estimated_tokens ?? 0) > 0) return budget.estimated_tokens
  const reference = budget.reference
  const referenceIsRepeated = reference?.scope === 'same_known_plan_and_request_path'
    && reference.confidence === 'repeated_complete_windows'
    && reference.sample_count >= 2
    && reference.account_count >= 2
  if (referenceIsRepeated && Number.isSafeInteger(reference?.estimated_tokens) && (reference?.estimated_tokens ?? 0) > 0) return reference.estimated_tokens
  return null
}

export function validateUsagePolicy(policy: AccountUsagePolicy, models: ModelTokenLimit[]) {
  if ([policy.reserve_credits, policy.daily_credits].some((value) => !Number.isFinite(value) || value < 0)) return 'usagePolicyInvalid'
  if (!Number.isSafeInteger(policy.daily_tokens) || policy.daily_tokens < 0) return 'usagePolicyInvalid'
  const seen = new Set<string>()
  for (const row of models) {
    const model = canonicalUsageModelKey(row.model)
    if (!model || seen.has(model) || !Number.isSafeInteger(row.tokens) || row.tokens <= 0) return 'usageModelInvalid'
    seen.add(model)
  }
  return ''
}

export function usagePolicyWithModels(policy: AccountUsagePolicy, models: ModelTokenLimit[]): AccountUsagePolicy {
  return { ...policy, model_daily_tokens: Object.fromEntries(models.map((row) => [canonicalUsageModelKey(row.model), row.tokens])) }
}
