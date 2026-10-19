import assert from 'node:assert/strict'
import test from 'node:test'
import { gatewayConfirmedCredits, officialAccountCredits } from '../src/lib/usageCredits.ts'

test('missing gateway credit observations are unknown, not zero', () => {
  assert.deepEqual(gatewayConfirmedCredits({ credits_actual: 0, credits_coverage: 'no_data', credits_known_attempts: 0, credits_unknown_attempts: 0 }), { kind: 'unknown' })
  assert.deepEqual(gatewayConfirmedCredits({ credits_actual: 0, credits_coverage: 'unknown', credits_known_attempts: 0, credits_unknown_attempts: 3 }), { kind: 'unknown' })
})

test('partial gateway observations expose only the confirmed subtotal and unknown count', () => {
  assert.deepEqual(gatewayConfirmedCredits({ credits_actual: 4.25, credits_coverage: 'partial', credits_known_attempts: 2, credits_unknown_attempts: 1 }), { kind: 'partial', amount: 4.25, unknownCount: 1 })
})

test('an explicitly confirmed zero remains visible', () => {
  assert.deepEqual(gatewayConfirmedCredits({ credits_actual: 0, credits_coverage: 'complete', credits_known_attempts: 1, credits_unknown_attempts: 0 }), { kind: 'amount', amount: 0 })
})

test('official empty, null, and unknown records stay unknown while an explicit zero stays zero', () => {
  assert.deepEqual(officialAccountCredits(undefined), { kind: 'unknown' })
  assert.deepEqual(officialAccountCredits({ status: 'unknown', credits: null }), { kind: 'unknown' })
  assert.deepEqual(officialAccountCredits({ status: 'complete', credits: 0 }), { kind: 'amount', amount: 0 })
  assert.deepEqual(officialAccountCredits({ status: 'partial', credits: 2.5, unknown_records: 3 }), { kind: 'partial', amount: 2.5, unknownCount: 3 })
})
