import assert from 'node:assert/strict'
import test from 'node:test'
import { checkinFeedback, dailyTasksDisabledReason } from '../src/lib/account.ts'

const account = { id: 'workbuddy-cn', provider: 'workbuddy', region: 'cn', enabled: true, auth_type: 'none' }

test('ready WorkBuddy credentials do not depend on legacy auth_type', () => {
  assert.equal(dailyTasksDisabledReason({ ...account, status: 'ready' }, ''), '')
  assert.equal(dailyTasksDisabledReason({ ...account, ready: true }, ''), '')
  assert.equal(dailyTasksDisabledReason({ ...account, hot: true }, ''), '')
})

test('missing login and explicit auth failures stay disabled despite stale readiness', () => {
  assert.equal(dailyTasksDisabledReason({ ...account, status: 'offline' }, ''), 'dailyTasksUnavailable')
  for (const status of ['login_required', 'auth_failed']) {
    assert.equal(dailyTasksDisabledReason({ ...account, status, ready: true }, ''), 'dailyTasksLoginRequired')
  }
  assert.equal(dailyTasksDisabledReason({ ...account, status: 'ready', runtime_state: 'auth_failed' }, ''), 'dailyTasksLoginRequired')
})

test('any account operation blocks another daily task submission', () => {
  for (const busy of ['daily', 'refresh', 'toggle', 'delete', 'checkin']) {
    assert.equal(dailyTasksDisabledReason({ ...account, status: 'ready' }, busy), 'dailyTasksBusy')
  }
})

test('chat cooldown and depleted quota do not block credentialed maintenance', () => {
  for (const status of ['cooling', 'quota_exhausted']) {
    assert.equal(dailyTasksDisabledReason({ ...account, status, ready: true }, ''), '')
    assert.equal(dailyTasksDisabledReason({ ...account, status, auth_type: 'native' }, ''), '')
  }
})

test('manual maintenance follows credential readiness independently of routing enablement', () => {
  assert.equal(dailyTasksDisabledReason({ ...account, enabled: false, status: 'ready' }, ''), '')
  assert.equal(dailyTasksDisabledReason({ ...account, enabled: false, status: 'offline' }, ''), 'dailyTasksUnavailable')
})

test('manual check-in confirms a new reward only for an explicit success outcome', () => {
  assert.deepEqual(checkinFeedback({ last_checkin_status: 'success', last_checkin_msg: ' confirmed by upstream ' }), {
    status: 'success', titleKey: 'checkinOutcomeSuccess', message: 'confirmed by upstream',
  })
  for (const result of [undefined, null, {}, { last_checkin_status: 'unknown' }, { last_checkin_msg: 'HTTP request completed' }]) {
    assert.deepEqual(checkinFeedback(result), {
      status: 'warning', titleKey: 'checkinOutcomeUnconfirmed', message: result?.last_checkin_msg || '',
    })
  }
})

test('manual check-in distinguishes an existing reward, unavailable activity, and failed claim', () => {
  for (const [outcome, tone, titleKey] of [
    ['already', 'default', 'checkinOutcomeAlready'],
    ['skipped', 'warning', 'checkinOutcomeSkipped'],
    ['error', 'danger', 'checkinOutcomeFailed'],
  ]) {
    assert.deepEqual(checkinFeedback({ last_checkin_status: outcome, last_checkin_msg: 'safe upstream detail' }), {
      status: tone, titleKey, message: 'safe upstream detail',
    })
  }
})
