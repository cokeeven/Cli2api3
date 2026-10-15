import { api } from './client'

export type DailyTaskRun = {
  id: string; account_id: string; run_date: string; trigger: string; status: string; attempt: number
  started_at: string; finished_at?: string; next_retry_at?: string
  report: { steps?: { step: string; status: string; ok: boolean; message?: string }[] }
}
export function runDailyTasks(id: string) {
  return api<{ status: string; summary: string; run: DailyTaskRun }>(`/api/accounts/${encodeURIComponent(id)}/daily-tasks`, { method: 'POST', body: '{}' })
}
export function fetchDailyTaskRuns(id: string) {
  return api<{ data: DailyTaskRun[] }>(`/api/accounts/${encodeURIComponent(id)}/daily-tasks?limit=20`)
}
