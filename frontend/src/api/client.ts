import { readStoredApiKey } from '@/hooks/useApiKey'

export class ApiError extends Error {
  status: number
  code?: string
  requestId?: string
  accountId?: string

  constructor(message: string, status: number, code?: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

export function isUnauthorized(err: unknown) {
  if (err instanceof ApiError) return err.status === 401 || err.code === 'invalid_api_key'
  const msg = err instanceof Error ? err.message : String(err || '')
  return /invalid_api_key|unauthorized|Missing\/invalid API key/i.test(msg)
}

export async function api<T = any>(path: string, opts: RequestInit = {}, keyOverride?: string): Promise<T> {
  return (await apiWithMetadata<T>(path, opts, keyOverride)).data
}

export async function apiWithMetadata<T = any>(path: string, opts: RequestInit = {}, keyOverride?: string): Promise<{ data: T; requestId: string; accountId: string }> {
  const headers = new Headers(opts.headers || {})
  if (opts.body && !(opts.body instanceof FormData) && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  const key = (keyOverride ?? readStoredApiKey()).trim()
  if (key && !headers.has('Authorization')) {
    headers.set('Authorization', `Bearer ${key}`)
  }
  const res = await fetch(path, { ...opts, headers })
  const requestId = res.headers.get('X-Request-Id') || ''
  const accountId = res.headers.get('X-CLI2API-Account') || res.headers.get('X-Qoder-Account') || ''
  const text = await res.text()
  let data: any
  try {
    data = text ? JSON.parse(text) : null
  } catch {
    data = text
  }
  if (!res.ok) {
    const raw = typeof data === 'string' ? data : text
    const looksHTML = /<!doctype html|<html[\s>]/i.test(raw)
    const msg = data?.error?.message || data?.message || (looksHTML ? `${res.status} ${res.statusText}` : (raw || res.statusText))
    const error = new ApiError(msg, res.status, data?.error?.code)
    error.requestId = requestId
    error.accountId = accountId
    throw error
  }
  return { data: data as T, requestId, accountId }
}
