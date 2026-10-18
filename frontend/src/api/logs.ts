import { api } from './client'

export type MaintenanceLog = {
  id: string
  account_id: string
  account_name: string
  provider: string
  region?: string
  kind: 'checkin' | 'daily_tasks' | 'credential' | 'quota'
  trigger: string
  status: string
  summary: string
  created_at: string
  finished_at?: string
  next_retry_at?: string
  attempt: number
  steps?: { step: string; status: string; ok: boolean; reused?: boolean; message?: string }[]
}

export type MaintenanceLogQuery = {
  account?: string; provider?: string; region?: string; kind?: string; trigger?: string; status?: string
  from?: string; to?: string; limit?: number; offset?: number
}

export function fetchMaintenanceLogs(query: MaintenanceLogQuery = {}) {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== '') params.set(key, String(value))
  }
  return api<{ items: MaintenanceLog[]; total: number; count: number; limit: number; offset: number }>(`/api/logs/maintenance?${params}`)
}

export type RequestAttempt = {
  provider?: string
  model?: string
  key_id?: string
  credits?: number | null
  usage_state?: string
  error_scope?: string
  route_reason?: string
  id: string
  request_id: string
  attempt_index: number
  account_id?: string
  started_at: string
  finished_at?: string | null
  status: string
  http_status?: number | null
  error_kind?: string
  error_message?: string
  latency_ms?: number | null
  prompt_tokens?: number | null
  completion_tokens?: number | null
  usage_source?: string
}

export type RequestStreamDiagnostic = {
  request_id: string
  created_at: string
  finished_at?: string | null
  upstream_status?: number | null
  upstream_request_id?: string
  context_err?: string
  cancellation_source?: string
  relay_error?: string
  sse_event_count: number
  bytes_read: number
  content_length?: number | null
  last_event?: string
  saw_done: boolean
  upstream_finish_reason?: string
  upstream_finish_reason_source?: string
  external_finish_reason?: string
  external_finish_reason_source?: string
  content_chars?: number | null
  reasoning_chars?: number | null
  tool_event_count?: number | null
  char_count_unit?: string
  cancellation_stage?: string
  prepare_duration_ms?: number | null
  upstream_response_duration_ms?: number | null
  first_event_duration_ms?: number | null
  first_content_duration_ms?: number | null
  timeout_phase?: string
  total_duration_ms?: number | null
  output_budget_enforcement?: string
}

export type RequestUsageDetail = {
  request_id: string
  created_at: string
  provider?: string
  credit?: number | null
  unit?: string
}

export type RequestLog = {
	routing_diagnostic?: {
	  initial_candidates: number; attempt_budget: number; attempted: number; stop_reason: string
	  final_selection_kind?: string; final_selection_code?: string
	  last_upstream_kind?: string; last_upstream_code?: string; last_upstream_scope?: string; last_account_id?: string
	  capacity_queue_status?: string; capacity_queue_wait_ms?: number; capacity_queue_position?: number
	  accounts_complete: boolean
	  accounts: { account_id: string; provider: string; region: string; reason: string; catalog_known: boolean; catalog_at?: string; quota_known: boolean; quota_at?: string; failure_kind?: string; failure_code?: string; failure_scope?: string }[]
	}
  id: string
  created_at: string
  finished_at?: string | null
  stream: boolean
  status: string
  requested_model: string
  mapped_model?: string
  requested_reasoning?: string
  resolved_reasoning?: string
  account_id?: string
  provider?: string
  routing?: string
  prompt_tokens?: number | null
  completion_tokens?: number | null
  cache_read_tokens?: number | null
  cache_write_tokens?: number | null
  usage_source?: string
  credits?: number | null
  latency_ms?: number | null
  ttfb_ms?: number | null
  error_kind?: string
  error_code?: string
  error_message?: string
  attempt_count: number
  client_request_id?: string
  source_fingerprint?: string
  output_limit_tokens?: number | null
  output_limit_source?: string
  output_budget_enforcement?: string
  prompt_stripped?: boolean | null
  message_count?: number
  message_shape_known: boolean
  empty_message_indexes?: number[]
  message_roles?: string[]
  attempts?: RequestAttempt[]
  stream_diagnostic?: RequestStreamDiagnostic
  usage_detail?: RequestUsageDetail
}

export type RequestLogList = {
  items: RequestLog[]
  total: number
  limit: number
  offset: number
}

export type RuntimeLogEntry = {
  id: number
  time: string
  level: string
  account_id?: string
  source?: string
  message: string
}

export type RuntimeLogList = {
  items: RuntimeLogEntry[]
  count: number
  total?: number
  limit?: number
  offset?: number
}

export type RequestStatsWindow = {
  from: string
  to: string
  hours: number
}

export type RequestStatsTotals = {
  requests: number
  ok: number
  incomplete: number
  error: number
  canceled: number
  streaming: number
  success_rate: number
}

export type RequestStatsLatency = {
  avg_ms?: number | null
  p50_ms?: number | null
  p95_ms?: number | null
  ttfb_avg_ms?: number | null
}

export type RequestStatsTokens = {
  prompt: number
  completion: number
  cache_read: number
  total: number
}

export type RequestStatsBucket = {
  key: string
  count: number
}

export type RequestStatsNamed = {
  key: string
  count: number
  ok: number
  error: number
  latency_avg_ms?: number | null
}

export type RequestStatsPoint = {
  at: string
  requests: number
  ok: number
  error: number
}

export type RequestStats = {
  window: RequestStatsWindow
  totals: RequestStatsTotals
  latency: RequestStatsLatency
  tokens: RequestStatsTokens
  status: RequestStatsBucket[]
  errors: RequestStatsBucket[]
  models: RequestStatsNamed[]
  accounts: RequestStatsNamed[]
  providers: RequestStatsNamed[]
  series: RequestStatsPoint[]
}

export type RequestLogQuery = {
  account?: string
  status?: string
  stream?: boolean
  error_kind?: string
  model?: string
  id?: string
  q?: string
  from?: string
  to?: string
  limit?: number
  offset?: number
}

export function fetchRequestStats(query: { hours?: number; from?: string; to?: string } = {}) {
  const params = new URLSearchParams()
  if (query.hours != null) params.set('hours', String(query.hours))
  if (query.from) params.set('from', query.from)
  if (query.to) params.set('to', query.to)
  const suffix = params.toString() ? `?${params}` : ''
  return api<RequestStats>(`/api/logs/stats${suffix}`)
}

export function fetchRequestLogs(query: RequestLogQuery = {}) {
  const params = new URLSearchParams()
  if (query.account) params.set('account', query.account)
  if (query.status) params.set('status', query.status)
  if (query.stream != null) params.set('stream', query.stream ? '1' : '0')
  if (query.error_kind) params.set('error_kind', query.error_kind)
  if (query.model) params.set('model', query.model)
  if (query.id) params.set('id', query.id)
  if (query.q) params.set('q', query.q)
  if (query.from) params.set('from', query.from)
  if (query.to) params.set('to', query.to)
  if (query.limit != null) params.set('limit', String(query.limit))
  if (query.offset != null) params.set('offset', String(query.offset))
  const suffix = params.toString() ? `?${params}` : ''
  return api<RequestLogList>(`/api/logs/requests${suffix}`)
}

export function fetchRequestLog(id: string) {
  return api<RequestLog>(`/api/logs/requests/${encodeURIComponent(id)}`)
}

export function clearRequestLogs() {
  return api<{ ok: boolean; deleted: number }>('/api/logs/requests', { method: 'DELETE' })
}

export function fetchRuntimeLogs(query: { after?: number; limit?: number; offset?: number; level?: string; q?: string; account?: string } = {}) {
  const params = new URLSearchParams()
  if (query.after != null) params.set('after', String(query.after))
  if (query.limit != null) params.set('limit', String(query.limit))
  if (query.offset != null) params.set('offset', String(query.offset))
  if (query.level) params.set('level', query.level)
  if (query.q) params.set('q', query.q)
  if (query.account) params.set('account', query.account)
  const suffix = params.toString() ? `?${params}` : ''
  return api<RuntimeLogList>(`/api/logs/runtime${suffix}`)
}
