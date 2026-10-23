import assert from 'node:assert/strict'
import test from 'node:test'
import { statsQuery } from '../src/lib/statsWindow.ts'
import { startVisibleInterval } from '../src/lib/statsRefresh.ts'

test('today uses browser-local midnight through the supplied current instant', () => {
  const now = new Date(2026, 9, 23, 0, 5, 30, 125)
  const query = statsQuery('today', null, now)
  const localMidnight = new Date(now)
  localMidnight.setHours(0, 0, 0, 0)
  assert.deepEqual(query, { from: localMidnight.toISOString(), to: now.toISOString() })
})

test('30-day window is a rolling 30 days ending at the current instant', () => {
  const now = new Date(2026, 9, 23, 12, 34, 56, 789)
  const query = statsQuery('720', null, now)
  assert.equal(Date.parse(query.to), now.getTime())
  assert.equal(Date.parse(query.to) - Date.parse(query.from), 30 * 24 * 60 * 60 * 1000)
})

test('custom window waits for a complete increasing range', () => {
  assert.equal(statsQuery('custom', null), null)
  assert.equal(statsQuery('custom', { from: '2026-10-23T00:00:00.000Z' }), null)
  assert.equal(statsQuery('custom', { from: 'bad', to: '2026-10-23T01:00:00.000Z' }), null)
  assert.equal(statsQuery('custom', {
    from: '2026-10-23T00:00:00.000Z', to: '2026-10-23T00:30:00.000Z',
  }).to, '2026-10-23T00:30:00.000Z')
  assert.equal(statsQuery('custom', {
    from: '2026-10-23T00:30:00.000Z', to: '2026-10-23T00:00:00.000Z',
  }), null)
})

test('visible auto-refresh stops while hidden and disposes its timer and listener', () => {
  const listeners = new Set()
  const doc = {
    hidden: false,
    addEventListener(name, callback) { if (name === 'visibilitychange') listeners.add(callback) },
    removeEventListener(name, callback) { if (name === 'visibilitychange') listeners.delete(callback) },
    changeVisibility(hidden) {
      this.hidden = hidden
      for (const callback of [...listeners]) callback()
    },
  }
  const active = new Map()
  let nextID = 0
  let cleared = 0
  const schedule = (callback, delay) => {
    const id = ++nextID
    active.set(id, { callback, delay })
    return id
  }
  const cancel = (id) => {
    if (active.delete(id)) cleared += 1
  }
  let ticks = 0
  const dispose = startVisibleInterval(doc, () => { ticks += 1 }, 30_000, schedule, cancel)
  assert.equal(active.size, 1)
  assert.equal(active.values().next().value.delay, 30_000)
  active.values().next().value.callback()
  assert.equal(ticks, 1)

  doc.changeVisibility(true)
  assert.equal(active.size, 0)
  doc.changeVisibility(false)
  assert.equal(active.size, 1)
  dispose()
  assert.equal(active.size, 0)
  assert.equal(listeners.size, 0)
  assert.equal(cleared, 2)
  doc.changeVisibility(true)
  doc.changeVisibility(false)
  assert.equal(active.size, 0)
})
