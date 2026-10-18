import test from 'node:test'
import assert from 'node:assert/strict'
import { accountRoutingFacts } from '../src/lib/account.ts'

test('runtime authentication, balance age and scheduling remain separate facts', () => {
  const row = { enabled: true, ready: true, provider: 'workbuddy', quota: { exceeded: true, fetched_at: '2026-10-17T00:00:00Z' } }
  assert.deepEqual(accountRoutingFacts(row), { authentication: 'authRuntimeReady', quotaTime: row.quota.fetched_at, scheduling: 'quota_exhausted' })
  assert.equal(accountRoutingFacts({ ...row, enabled: false }).scheduling, 'disabled')
  assert.equal(accountRoutingFacts({ ...row, status: 'auth_failed' }).authentication, 'authNeedsLogin')
  assert.equal(accountRoutingFacts({ enabled: true }).quotaTime, null)
  assert.equal(accountRoutingFacts({ enabled: true }).authentication, 'authUnknown')
})
