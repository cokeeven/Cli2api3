import assert from 'node:assert/strict'
import test from 'node:test'
import { usagePolicyWithModels, validateUsagePolicy } from '../src/lib/usagePolicy.ts'

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
