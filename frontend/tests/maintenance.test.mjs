import assert from 'node:assert/strict'
import test from 'node:test'
import { maintenanceActionDisabled, quotaSnapshotStale } from '../src/lib/maintenance.ts'

test('maintenance actions preserve provider scope, disabled accounts and task readiness', () => {
  const ready = { id: 'synthetic', provider: 'workbuddy', region: 'cn', enabled: true, ready: true }
  for (const action of ['maintain', 'refresh?quota=1', 'checkin', 'daily-tasks']) assert.equal(maintenanceActionDisabled(ready, action, ''), false)
  assert.equal(maintenanceActionDisabled({ ...ready, enabled: false }, 'maintain', ''), true)
  assert.equal(maintenanceActionDisabled({ ...ready, provider: 'qoder' }, 'maintain', ''), true)
  assert.equal(maintenanceActionDisabled({ ...ready, region: 'global' }, 'checkin', ''), true)
  assert.equal(maintenanceActionDisabled({ ...ready, provider: 'trae' }, 'daily-tasks', ''), true)
  assert.equal(maintenanceActionDisabled(ready, 'maintain', 'refresh'), true)
})

test('quota freshness preserves unknown and accounts for configured jitter', () => {
  const now = Date.parse('2026-10-17T00:00:00Z')
  assert.equal(quotaSnapshotStale(undefined, 30, now), null)
  assert.equal(quotaSnapshotStale('invalid', 30, now), null)
  assert.equal(quotaSnapshotStale(new Date(now - 32 * 60000).toISOString(), 30, now), false)
  assert.equal(quotaSnapshotStale(new Date(now - 34 * 60000).toISOString(), 30, now), true)
})
