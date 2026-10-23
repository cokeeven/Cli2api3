import test from 'node:test'
import assert from 'node:assert/strict'
import { accountCooldownLabel, accountRoutingFacts, accountState, isAvailable } from '../src/lib/account.ts'

test('an expired account cooldown falls back to current readiness', () => {
  const row = {
    enabled: true,
    status: 'cooling',
    runtime_state: 'ready',
    ready: true,
    hot: true,
    cooldown_until: '2020-01-01T00:00:00Z',
  }

  assert.equal(accountState(row), 'hot')
  assert.equal(isAvailable(row), true)
  assert.deepEqual(accountRoutingFacts(row), {
    authentication: 'authRuntimeReady',
    quotaTime: null,
    scheduling: 'hot',
  })
})

test('cooldown stays active when its deadline is future or unknown', () => {
  assert.equal(accountState({ enabled: true, status: 'cooling' }), 'cooling')
  assert.equal(accountState({
    enabled: true,
    status: 'ready',
    ready: true,
    cooldown_until: new Date(Date.now() + 60_000).toISOString(),
  }), 'cooling')

  const fallbackDeadline = new Date(Date.now() + 5 * 60_000).toISOString()
  assert.equal(accountCooldownLabel({ down_until: '2020-01-01T00:00:00Z', cooldown_until: fallbackDeadline }), '5m')
})

test('an expired cooldown does not hide a separate authentication failure', () => {
  assert.equal(accountState({
    enabled: true,
    status: 'cooling',
    runtime_state: 'auth_failed',
    cooldown_until: '2020-01-01T00:00:00Z',
  }), 'auth_failed')
})
