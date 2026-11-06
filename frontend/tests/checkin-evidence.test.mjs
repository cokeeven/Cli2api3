import test from 'node:test'
import assert from 'node:assert/strict'
import { formatCheckinMessage } from '../src/lib/account.ts'

test('check-in diagnostics display distinct safe facts and retain legacy text', () => {
  const t = (key, vars) => vars ? `${key}:${vars.count}` : key
  const result = formatCheckinMessage('outcome=upstream_rejected_9074; phase=status; attempts=1; http=200; code=9074; reward=unconfirmed', t)
  for (const fact of ['checkinDetail_upstream_rejected_9074', 'checkinPhase_status', 'HTTP 200', '9074', 'checkinAttempts:1', 'checkinRewardUnconfirmed']) assert.ok(result.includes(fact))
  assert.equal(formatCheckinMessage('legacy unknown', t), 'legacy unknown')
})

test('confirmed check-in without amount remains reward-unconfirmed in the UI', () => {
  const t = (key, vars) => vars ? `${key}:${vars.count}` : key
  const result = formatCheckinMessage('checkin claimed; outcome=success; phase=status; attempts=3; http=200; code=0; reward=unconfirmed', t)
  assert.ok(result.includes('checkinDetail_success'))
  assert.ok(result.includes('checkinRewardUnconfirmed'))
  assert.ok(!result.includes('reward_credits'))
})
