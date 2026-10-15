import assert from 'node:assert/strict'
import test from 'node:test'
import { attemptCreditAmount, attemptRouteKey, attemptScopeKey, attemptUsageKey } from '../src/lib/attemptFacts.ts'

test('missing and invalid attempt costs never become a zero-credit charge', () => {
  for (const value of [undefined, null, NaN, Infinity, -1]) assert.equal(attemptCreditAmount(value), null)
  assert.equal(attemptCreditAmount(0), '0')
  assert.equal(attemptCreditAmount(1.25), '1.25')
})

test('attempt facts keep quota migration distinct from temporary borrowing', () => {
  assert.equal(attemptRouteKey('temporary_capacity_borrow'), 'logsAttemptRouteBorrow')
  assert.equal(attemptRouteKey('quota_migration'), 'logsAttemptRouteMigration')
  assert.equal(attemptScopeKey('model'), 'logsAttemptScopeModel')
  assert.equal(attemptUsageKey('actual'), 'logsAttemptUsageActual')
  assert.equal(attemptUsageKey(undefined), 'logsAttemptUsageUnknown')
  assert.equal(attemptRouteKey('legacy'), '')
})
