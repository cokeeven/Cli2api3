import { api } from './client'
import type { AccountQuotaPackage } from './types'

export type AccountUsagePolicy = {
  reserve_credits: number
  daily_tokens: number
  daily_credits: number
  model_daily_tokens: Record<string, number>
  prefer_expiring: boolean
}

export type DailyUsage = {
  tokens_actual: number
  tokens_reserved: number
  credits_actual: number
  credits_reserved: number
  unknown_attempts: number
  estimated_attempts: number
  attempts: number
}

export type AccountUsage = {
  quota_history?: Array<{ fetched_at: string; available: number; expiring_7d: number; expiring_30d: number; unit: string; known: boolean }>
  guard_states?: Array<{ reason: string; scope: string; model?: string; reset_at?: string }>
  account_id: string
  policy: AccountUsagePolicy
  day: string
  today: DailyUsage
  quota: {
    known?: boolean
    unit?: string
    available?: number
    expiring_7d?: number
    expiring_30d?: number
    expired?: number
    unknown_expiry?: number
    fetched_at?: string
    packages?: Array<AccountQuotaPackage & { id?: string; source?: string; status?: string }>
  }
  series: Array<DailyUsage & { day: string }>
  recording_since?: string
  credit_balance_pending?: number
  credit_balance_unbounded?: number
}

export function fetchAccountUsage(id: string, days = 7) {
  return api<AccountUsage>(`/api/accounts/${encodeURIComponent(id)}/usage?days=${days}`)
}

export function updateAccountUsagePolicy(id: string, input: AccountUsagePolicy) {
  return api<AccountUsagePolicy>(`/api/accounts/${encodeURIComponent(id)}/usage-policy`, { method: 'PUT', body: JSON.stringify(input) })
}

export function reconcileAccountUsage(id: string, snapshotFetchedAt: string) {
  return api<{ covered_attempts: number }>(`/api/accounts/${encodeURIComponent(id)}/usage-reconcile`, {
    method: 'POST', body: JSON.stringify({ snapshot_fetched_at: snapshotFetchedAt, confirm_covered: true }),
  })
}
