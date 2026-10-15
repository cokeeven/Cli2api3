export function attemptCreditAmount(credits?: number | null) {
  if (credits == null || !Number.isFinite(credits) || credits < 0) return null
  return String(Math.round(credits * 10000) / 10000)
}

export function attemptRouteKey(reason?: string) {
  const keys: Record<string, string> = {
    default: 'logsAttemptRouteDefault', priority: 'logsAttemptRoutePriority', sticky: 'logsAttemptRouteSticky',
    temporary_capacity_borrow: 'logsAttemptRouteBorrow', quota_migration: 'logsAttemptRouteMigration',
  }
  return keys[reason || ''] || ''
}

export function attemptScopeKey(scope?: string) {
  const keys: Record<string, string> = {
    model: 'logsAttemptScopeModel', account: 'logsAttemptScopeAccount', platform: 'logsAttemptScopePlatform', request: 'logsAttemptScopeRequest',
  }
  return keys[scope || ''] || ''
}

export function attemptUsageKey(state?: string) {
  const keys: Record<string, string> = {
    actual: 'logsAttemptUsageActual', estimated: 'logsAttemptUsageEstimated', unknown: 'logsAttemptUsageUnknown', non_billable: 'logsAttemptUsageNonBillable',
  }
  return keys[state || ''] || 'logsAttemptUsageUnknown'
}
