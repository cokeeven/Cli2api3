import assert from 'node:assert/strict'
import test from 'node:test'
import { createLoginDelay, pollLogin } from '../src/lib/loginPoll.ts'

test('cancelling a login timer settles the sleeping poll', async () => {
  const delay = createLoginDelay()
  const pending = delay.wait(60000)
  delay.cancel()
  assert.equal(await pending, false)
})

test('starting a new login delay releases the superseded delay', async () => {
  const delay = createLoginDelay()
  const previous = delay.wait(60000)
  const current = delay.wait(0)
  assert.equal(await previous, false)
  assert.equal(await current, true)
  delay.cancel()
})

function deps(overrides = {}) {
  const messages = []
  return {
    messages,
    value: {
      attempts: 3,
      sleep: async () => true,
      fetchStatus: async () => ({ login: { status: 'pending' } }),
      isCancelled: () => false,
      onMessage: (message) => messages.push(message),
      timeoutMessage: 'timed out',
      ...overrides,
    },
  }
}

test('a pending login keeps polling until the provider reports ok', async () => {
  let calls = 0
  const { value } = deps({
    fetchStatus: async () => {
      calls += 1
      return calls < 3 ? { login: { status: 'pending', message: `step ${calls}` } } : { login: { status: 'ok' } }
    },
  })
  const result = await pollLogin(value)
  assert.deepEqual(result, { kind: 'ok' })
  assert.equal(calls, 3)
})

test('a provider error is reported so the wizard can return to an editable state', async () => {
  const { value } = deps({ fetchStatus: async () => ({ login: { status: 'error', message: 'callback rejected' } }) })
  assert.deepEqual(await pollLogin(value), { kind: 'error', message: 'callback rejected' })
})

test('an exhausted attempt budget reports the timeout message', async () => {
  const { value } = deps({ attempts: 2 })
  assert.deepEqual(await pollLogin(value), { kind: 'error', message: 'timed out' })
})

test('cancelling stops polling and never reports the superseded outcome', async () => {
  let cancelled = false
  let calls = 0
  const { value } = deps({
    // The sleep resolves false once the session was abandoned, mirroring a
    // cleared poll timer.
    sleep: async () => !cancelled,
    fetchStatus: async () => {
      calls += 1
      return { login: { status: 'pending' } }
    },
    isCancelled: () => cancelled,
  })
  const pending = pollLogin(value)
  cancelled = true
  assert.deepEqual(await pending, { kind: 'cancelled' })
  assert.ok(calls <= 1)
})

test('a late status arriving after cancellation is discarded', async () => {
  let cancelled = false
  let resolveStatus
  const { value, messages } = deps({
    fetchStatus: () =>
      new Promise((resolve) => {
        resolveStatus = resolve
      }),
    isCancelled: () => cancelled,
  })
  const pending = pollLogin(value)
  await Promise.resolve()
  // The operator abandoned the attempt while the status call was in flight.
  cancelled = true
  resolveStatus({ login: { status: 'ok', message: 'late success' } })
  assert.deepEqual(await pending, { kind: 'cancelled' })
  assert.deepEqual(messages, [], 'a cancelled attempt must not publish messages')
})

test('a zero attempt budget times out without touching the provider', async () => {
  let calls = 0
  const { value } = deps({ attempts: 0, fetchStatus: async () => { calls += 1; return {} } })
  assert.deepEqual(await pollLogin(value), { kind: 'error', message: 'timed out' })
  assert.equal(calls, 0)
})
