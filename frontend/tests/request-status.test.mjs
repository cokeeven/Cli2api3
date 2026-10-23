import assert from 'node:assert/strict'
import test from 'node:test'
import { hasUnfinishedRequestLogStatus } from '../src/lib/requestStatus.ts'

test('request history lifecycle labels do not claim live execution', () => {
  for (const status of ['started', 'streaming', ' STREAMING ']) {
    assert.equal(hasUnfinishedRequestLogStatus(status), true)
  }
  for (const status of ['ok', 'error', 'canceled', 'incomplete', undefined, null]) {
    assert.equal(hasUnfinishedRequestLogStatus(status), false)
  }
})
