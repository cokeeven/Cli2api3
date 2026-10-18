import type { Overview } from '../api/types.ts'
import { dailyTasksDisabledReason } from './account.ts'

type Account = NonNullable<Overview['accounts']>[number]
export function maintenanceActionDisabled(account: Account | undefined, action: string, busy: string) {
  if (!account || !account.enabled || busy) return true
  if (account.provider !== 'workbuddy' && !(account.provider === 'trae' && account.region === 'cn')) return true
  if (action === 'checkin' && account.region !== 'cn') return true
  if (action === 'daily-tasks' && (account.provider !== 'workbuddy' || account.region !== 'cn' || Boolean(dailyTasksDisabledReason(account, busy)))) return true
  return false
}

export function quotaSnapshotStale(fetchedAt: string | undefined, intervalMinutes: number, now = Date.now()): boolean | null {
  const fetched = fetchedAt ? Date.parse(fetchedAt) : NaN
  if (!Number.isFinite(fetched)) return null
  return now > fetched + intervalMinutes * 60000 * 1.1
}
