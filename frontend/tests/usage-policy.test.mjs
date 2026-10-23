import assert from 'node:assert/strict'
import test from 'node:test'
import { canonicalUsageModelKey, hasModelUsageLimit, safeBudgetGuardSuggestion, usagePolicyWithModels, validateUsagePolicy } from '../src/lib/usagePolicy.ts'

const policy = { reserve_credits: 0, daily_tokens: 0, daily_credits: 0, model_daily_tokens: {}, prefer_expiring: false }

test('all guards can remain disabled without adding a model restriction', () => {
  assert.equal(validateUsagePolicy(policy, []), '')
  assert.deepEqual(usagePolicyWithModels(policy, []).model_daily_tokens, {})
})

test('guard validation rejects invalid token quantities and unsafe credit amounts', () => {
  for (const daily_tokens of [-1, 1.1, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1]) {
    assert.equal(validateUsagePolicy({ ...policy, daily_tokens }, []), 'usagePolicyInvalid')
  }
  for (const reserve_credits of [-1, NaN, Infinity]) {
    assert.equal(validateUsagePolicy({ ...policy, reserve_credits }, []), 'usagePolicyInvalid')
  }
  assert.equal(validateUsagePolicy({ ...policy, daily_credits: 0.125 }, []), '')
})

test('canonical duplicate model IDs cannot silently overwrite a limit', () => {
  assert.equal(validateUsagePolicy(policy, [{ model: 'KIMI_K3', tokens: 100 }, { model: 'kimi k3', tokens: 200 }]), 'usageModelInvalid')
  for (const row of [{ model: '', tokens: 1 }, { model: 'kimi-k3', tokens: 0 }, { model: 'kimi-k3', tokens: 1.5 }]) {
    assert.equal(validateUsagePolicy(policy, [row]), 'usageModelInvalid')
  }
  assert.deepEqual(usagePolicyWithModels(policy, [{ model: ' KIMI_K3 ', tokens: 123 }]).model_daily_tokens, { 'kimi-k3': 123 })
  assert.deepEqual(usagePolicyWithModels(policy, [{ model: 'workbuddy/KIMI_K3', tokens: 123 }]).model_daily_tokens, { 'kimi-k3': 123 })
  assert.deepEqual(usagePolicyWithModels(policy, [{ model: 'command/deepseek/model-name', tokens: 123 }]).model_daily_tokens, { 'deepseek/model-name': 123 })
})

test('budget suggestion only pre-fills repeated complete evidence and preserves any existing entry', () => {
  const repeatedOwn = { estimated_tokens: 1200, complete_windows: 2, observation_truncated: false, basis: 'explicit_6004_reset_windows_weighted_recent' }
  assert.equal(safeBudgetGuardSuggestion(repeatedOwn), 1200)
  assert.equal(safeBudgetGuardSuggestion({ ...repeatedOwn, complete_windows: 1 }), null)
  assert.equal(safeBudgetGuardSuggestion({ ...repeatedOwn, observation_truncated: true }), null)
  assert.equal(safeBudgetGuardSuggestion({
    ...repeatedOwn,
    estimated_tokens: null,
    reference: { estimated_tokens: 800, sample_count: 3, account_count: 2, confidence: 'repeated_complete_windows', scope: 'same_known_plan_and_request_path' },
  }), 800)
  assert.equal(safeBudgetGuardSuggestion({
    ...repeatedOwn,
    estimated_tokens: null,
    reference: { estimated_tokens: 800, sample_count: 3, account_count: 2, confidence: 'repeated_complete_windows', scope: 'same_region_model_plan_unverified' },
  }), null)
  assert.equal(safeBudgetGuardSuggestion({
    ...repeatedOwn,
    estimated_tokens: null,
    reference: { estimated_tokens: 800, sample_count: 1, account_count: 1, confidence: 'single_complete_window', scope: 'same_known_plan_and_request_path' },
  }), null)

  const configuredZero = { ...policy, model_daily_tokens: { 'glm-5.3': 0 } }
  assert.equal(canonicalUsageModelKey('WorkBuddy/GLM_5.3'), 'glm-5.3')
  assert.equal(hasModelUsageLimit(configuredZero, 'glm-5.3'), true)
  assert.equal(hasModelUsageLimit(policy, 'glm-5.3'), false)
})
