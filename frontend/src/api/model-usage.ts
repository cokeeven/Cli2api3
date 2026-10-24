import { api } from './client'

export type ModelUsageRow = {
  provider: string
  region: string
  account_id: string
  model: string
  requests: number
  attempts: number
  succeeded_attempts: number
  failed_attempts: number
  canceled_attempts: number
  failover_attempts: number
  length_requests: number
  prompt_tokens: number | null
  completion_tokens: number | null
  known_usage_attempts: number
  estimated_usage_attempts: number
  missing_usage_attempts: number
  last_success_at?: string | null
  cache_read_tokens: number | null
  cache_write_tokens: number | null
  cache_known_requests: number
  cache_missing_requests: number
}

export type ModelUsageError = {
  at: string
  request_id: string
  attempt_id: string
  provider: string
  region: string
  account_id: string
  model: string
  status: string
  kind?: string
  code?: string
  scope?: string
  finish_reason?: string
  finish_reason_source?: string
  upstream_reset_at?: string | null
}

export type BudgetReference = {
  estimated_tokens: number | null
  sample_count: number
  account_count: number
  sample_min_tokens: number | null
  sample_max_tokens: number | null
  sample_from?: string
  sample_to?: string
  plan?: string
  request_path?: string
  confidence: string
  scope: string
  basis: string
}

export type BudgetEstimate = {
  account_id: string
  region: string
  model: string
  plan?: string
  request_path?: string
  sample_count: number
  complete_windows: number
  sample_min_tokens: number | null
  sample_max_tokens: number | null
  sample_from?: string
  sample_to?: string
  statistical_days: number
  statistical_timezone: string
  weight_rule: string
  sample_freshness: string
  estimated_tokens: number | null
  observed_since_reset: number | null
  estimated_remaining: number | null
  observed_24h_tokens: number | null
  estimated_24h_budget_tokens: number | null
  observed_24h_coverage: string
  observed_24h_missing_attempts: number
  observed_24h_from?: string
  observed_24h_to?: string
  estimated_24h_remaining: number | null
  estimated_24h_basis?: string
  estimated_at?: string
  first_limit_24h_sample?: BudgetObservationSample
  reset_at?: string
  restriction_observed_at?: string
  restricted: boolean
  restriction_source?: string
  cooling: boolean
  coverage_complete: boolean
  coverage_state: string
  missing_attempts: number
  observation_truncated: boolean
  basis: string
  reference?: BudgetReference
  weak_references?: BudgetReference[]
  weak_references_truncated: boolean
  low_confidence_references?: Array<{
    account_id: string
    plan?: string
    request_path?: string
    scope: string
    sample: BudgetObservationSample
  }>
  low_confidence_references_truncated: boolean
}

export type BudgetObservationSample = {
  tokens: number | null
  from?: string
  to?: string
  missing_attempts: number
  coverage: string
  confidence: string
  source: string
}

export type ModelUsageAccount = {
  id: string
  name: string
  provider: string
  region: string
  enabled: boolean
  status: string
  ready: boolean
  hot: boolean
  in_flight: number
  max_in_flight: number
  down_until?: string
  quota?: {
    used: number
    total: number
    remaining: number
    percentage: number
    unit: string
    exceeded: boolean
    fetched_at: string
    plan?: string
  }
  quota_source?: string
  model_budget_tag: string
}

export type RouteCandidate = {
  id: string
  provider: string
  region: string
  reason: string
  schedulable: boolean
  catalog_known: boolean
  model_cooldown_until?: string
  account_cooldown_until?: string
  model_cooldown_evidence?: RouteCooldownEvidence
  account_cooldown_evidence?: RouteCooldownEvidence
  account_quota_evidence?: RouteQuotaEvidence
  quota_known: boolean
  quota_remaining?: number
  quota_unit?: string
  quota_fetched_at?: string
  in_flight: number
  max_in_flight?: number
}

export type RouteCooldownEvidence = {
  first_observed_at?: string
  last_observed_at?: string
  code?: string
  source?: string
  scope?: string
  reported_scope?: string
  upstream_reset_at?: string
  local_backoff_until?: string
}

export type RouteQuotaEvidence = Omit<RouteCooldownEvidence, 'local_backoff_until'>

export type ModelUsageResponse = {
  data: {
    rows: ModelUsageRow[]
    recent: ModelUsageError[]
    from: string
    to: string
    budget_observation_from?: string
    truncated: boolean
    budget_observations_truncated: boolean
  }
  accounts: ModelUsageAccount[]
  routes: { provider: string; region: string; model: string; candidates: RouteCandidate[] }[]
  workbuddy_budgets: BudgetEstimate[] | null
  workbuddy_budgets_total: number
  workbuddy_budgets_offset: number
  workbuddy_budgets_limit: number
  workbuddy_budgets_has_more: boolean
  workbuddy_budget_catalog_truncated: boolean
  budget_observation_complete: boolean
  source: string
}

export type ModelUsageQuery = {
  provider?: string
  region?: string
  model?: string
  account?: string
  from?: string
  to?: string
  limit?: number
  budget_limit?: number
  budget_offset?: number
}

export type ModelUsageOverviewRow = {
  provider: string
  region: string
  model: string
  requests: number
  attempts: number
  succeeded_attempts: number
  failed_attempts: number
  canceled_attempts: number
  failover_attempts: number
  length_requests: number
  prompt_tokens: number | null
  completion_tokens: number | null
  known_usage_attempts: number
  estimated_usage_attempts: number
  missing_usage_attempts: number
  account_count: number
  recent_usage: boolean
  last_activity_at?: string
  schedulable_accounts: number
  restricted_accounts: number
}

export type ModelUsageOverviewResponse = {
  data: {
    rows: ModelUsageOverviewRow[]
    summary: {
      models: number
      requests: number
      attempts: number
      prompt_tokens: number
      completion_tokens: number
      known_usage_attempts: number
      estimated_usage_attempts: number
      missing_usage_attempts: number
    }
    total: number
    page: number
    page_size: number
    has_more: boolean
    from: string
    to: string
    catalog_limited: boolean
  }
  source: string
}

export type ModelUsageDetailRow = {
  usage: Pick<ModelUsageRow,
    'account_id' | 'requests' | 'attempts' | 'succeeded_attempts' | 'failed_attempts' | 'canceled_attempts' |
    'failover_attempts' | 'length_requests' | 'prompt_tokens' | 'completion_tokens' | 'known_usage_attempts' |
    'estimated_usage_attempts' | 'missing_usage_attempts' | 'last_success_at' | 'cache_read_tokens' |
    'cache_write_tokens' | 'cache_known_requests' | 'cache_missing_requests'>
  account: ModelUsageAccount
  route?: RouteCandidate
  budgets: BudgetEstimate[]
}

export type ModelUsageDetailResponse = {
  data: {
    rows: ModelUsageDetailRow[]
    total: number
    page: number
    page_size: number
    has_more: boolean
    from: string
    to: string
    budget_observation_from?: string
    budget_observations_truncated: boolean
    catalog_limited: boolean
  }
  source: string
}

export type ModelUsageDiagnosticsResponse = {
  data: {
    errors: ModelUsageError[]
    total: number
    page: number
    page_size: number
    has_more: boolean
    from: string
    to: string
  }
  routes: RouteCandidate[]
  routes_total: number
  routes_page: number
  routes_page_size: number
  routes_has_more: boolean
  source: string
}

export type ModelUsageCompactQuery = {
  provider?: string
  region?: string
  model?: string
  account?: string
  search?: string
  activity?: string
  from?: string
  to?: string
  page?: number
  page_size?: number
  sort?: string
}

export type ModelProbeResult = {
  ok: boolean
  request_id: string
  provider: string
  region: string
  account_id: string
  observed_account_id?: string
  model: string
  requested_max_tokens: number
  output: string
  finish_reason: string
  finish_reason_source: string
  prompt_tokens: number | null
  completion_tokens: number | null
  usage_source: string
  build_fingerprint: string
  error_code?: string
  error_message?: string
}

export function fetchModelUsage(query: ModelUsageQuery = {}, signal?: AbortSignal) {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== '') params.set(key, String(value))
  }
  const suffix = params.toString()
  return api<ModelUsageResponse>(`/api/model-usage${suffix ? `?${suffix}` : ''}`, { signal })
}

function compactQuery(query: ModelUsageCompactQuery) {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== '') params.set(key, String(value))
  }
  return params.toString()
}

export function fetchModelUsageOverview(query: ModelUsageCompactQuery, signal?: AbortSignal) {
  const suffix = compactQuery(query)
  return api<ModelUsageOverviewResponse>(`/api/model-usage/overview${suffix ? `?${suffix}` : ''}`, { signal })
}

export function fetchModelUsageDetail(query: ModelUsageCompactQuery, signal?: AbortSignal) {
  const suffix = compactQuery(query)
  return api<ModelUsageDetailResponse>(`/api/model-usage/detail${suffix ? `?${suffix}` : ''}`, { signal })
}

export function fetchModelUsageDiagnostics(query: ModelUsageCompactQuery, signal?: AbortSignal) {
  const suffix = compactQuery(query)
  return api<ModelUsageDiagnosticsResponse>(`/api/model-usage/diagnostics${suffix ? `?${suffix}` : ''}`, { signal })
}

export function probeModel(accountId: string, model: string, signal?: AbortSignal) {
  return api<ModelProbeResult>('/api/model-usage/probe', {
    method: 'POST',
    body: JSON.stringify({ account_id: accountId, model }),
    signal,
  })
}
