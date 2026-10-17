import test from 'node:test'
import assert from 'node:assert/strict'
import { busyStates, applyAgentStates, progressAgentStates, updateFailed } from '../src/lib/updateState.ts'

test('native preparation never counts as confirmed installation', () => {
  for (const state of ['downloading', 'verifying', 'extracting']) {
    assert.equal(busyStates.has(state), true)
    assert.equal(progressAgentStates.has(state), true)
    assert.equal(applyAgentStates.has(state), false)
  }
  assert.equal(busyStates.has('ready_to_apply'), false)
  assert.equal(applyAgentStates.has('ready_to_apply'), false)
})
test('native replacement and rollback remain busy across a page refresh', () => {
  for (const state of ['stopping', 'replacing', 'starting', 'rolling_back']) {
    assert.equal(busyStates.has(state), true)
    assert.equal(applyAgentStates.has(state), true)
  }
})
test('download, verification and recovery failures are terminal and visible', () => {
  for (const state of ['download_failed', 'verification_failed', 'recovery_failed', 'rolled_back']) {
    assert.equal(updateFailed(state), true)
    assert.equal(busyStates.has(state), false)
  }
  assert.equal(updateFailed('succeeded'), false)
})
