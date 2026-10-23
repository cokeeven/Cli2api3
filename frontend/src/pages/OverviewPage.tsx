import { lazy, Suspense, useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import gsap from 'gsap'
import { Link } from 'react-router-dom'
import { Button, Card, Chip } from '@heroui/react'
import {
  ArrowUpRight,
  Cpu,
  Cube,
  Pause,
  Play,
  Pulse,
} from '@phosphor-icons/react'
import { fetchRequestStats, type RequestStats } from '@/api/logs'
import { fetchSystemResources, type SystemResources } from '@/api/system'
import type { Overview } from '@/api/types'
import { CountUp } from '@/components/overview/CountUp'
import { TrafficChartEmpty } from '@/components/overview/TrafficChartEmpty'

const TrafficChart = lazy(() => import('@/components/overview/TrafficChart').then((mod) => ({ default: mod.TrafficChart })))
import { FilterToggle } from '@/components/ui/FilterToggle'
import { TimeRangePicker } from '@/components/ui/TimeRangePicker'
import { PageAlert } from '@/components/ui/PageAlert'
import { OverviewPageSkeleton, RankListSkeleton, SkeletonBlock, TrafficChartSkeleton } from '@/components/ui/PageSkeletons'
import { useI18n } from '@/hooks/useI18n'
import { useOverview } from '@/hooks/useOverview'
import { fetchAccounts, fetchModels } from '@/api/overview'
import { formatBytes, formatCompact, formatLatency, formatPercent } from '@/lib/format'
import { accountProviderFamilyLabel, accountProviderLabel } from '@/lib/provider'
import { startVisibleInterval } from '@/lib/statsRefresh'
import { STATS_WINDOW_LABEL, statsQuery, type StatsWindow } from '@/lib/statsWindow'
import { dateValueToISO, type DateRangeValue } from '@/lib/timeRange'
import { ProviderMark } from '@/components/ProviderMark'

type AccountRow = NonNullable<Overview['accounts']>[number]

const EMPTY_STATS: RequestStats = {
  window: { from: '', to: '', hours: 24 },
  totals: { requests: 0, ok: 0, incomplete: 0, error: 0, canceled: 0, streaming: 0, success_rate: 0 },
  latency: {},
  tokens: { prompt: 0, completion: 0, cache_read: 0, total: 0 },
  status: [],
  errors: [],
  models: [],
  accounts: [],
  providers: [],
  series: [],
}

function familyLabel(provider: string | undefined, t: (key: string) => string) {
  if (!provider || provider === '(unknown)') return t('statsUnknown')
  return accountProviderFamilyLabel(provider, t)
}

function accountLabel(accounts: AccountRow[], id: string, unassigned: string) {
  if (!id || id === '(unassigned)') return unassigned
  return accounts.find((account) => account.id === id)?.name || id
}

function errorLabel(kind: string, t: (key: string) => string) {
  const keys: Record<string, string> = {
    quota: 'logsKindQuota',
    rate_limit: 'logsKindRateLimit',
    concurrency_capacity: 'logsKindConcurrencyCapacity',
    auth: 'logsKindAuth',
    not_ready: 'logsKindNotReady',
    unavailable: 'logsKindUnavailable',
    invalid_request: 'logsKindInvalidRequest',
    model_not_available: 'logsKindModelNotAvailable',
  }
  return keys[kind] ? t(keys[kind]) : kind
}

function quotaTone(percentage?: number, exceeded?: boolean) {
  if (exceeded) return 'danger'
  if ((percentage ?? 0) >= 80) return 'warning'
  return 'ok'
}

export function OverviewPage() {
  const { lang, t } = useI18n()
  const { overview, loading, error } = useOverview()
  const [statsWindow, setStatsWindow] = useState<StatsWindow>('today')
  const [customRange, setCustomRange] = useState<DateRangeValue | null>(null)
  const [statsAutoRefreshPaused, setStatsAutoRefreshPaused] = useState(false)
  const [stats, setStats] = useState<RequestStats | null>(null)
  const [statsError, setStatsError] = useState('')
  const [statsLoading, setStatsLoading] = useState(true)
  const inFlightStatsKeys = useRef(new Set<string>())
  const statsMounted = useRef(false)
  const [accounts, setAccounts] = useState<NonNullable<Overview['accounts']>>([])
  const [accountsLoading, setAccountsLoading] = useState(true)
  const [modelCount, setModelCount] = useState(0)
  const [resources, setResources] = useState<SystemResources | null>(null)

  const proxyOk = Boolean(overview?.proxy?.ok)
  const workerOk = Boolean(overview?.worker?.ok)
  const readyAccounts = overview?.worker?.ready_count ?? 0
  const hotAccounts = overview?.worker?.hot_count ?? 0
  const coolingAccounts = overview?.worker?.cooling_count ?? 0
  const inFlight = overview?.worker?.in_flight ?? 0
  const traffic = stats ?? EMPTY_STATS
  const customStatsRange = statsWindow === 'custom' ? {
    from: dateValueToISO(customRange?.start),
    to: dateValueToISO(customRange?.end, true),
  } : null
  const selectedStatsQuery = statsQuery(statsWindow, customStatsRange)
  const statsRangeReady = selectedStatsQuery !== null
  const statsSelectionKey = statsWindow === 'custom'
    ? `custom:${customStatsRange?.from ?? ''}|${customStatsRange?.to ?? ''}`
    : statsWindow
  const statsSelectionRef = useRef(statsSelectionKey)
  useLayoutEffect(() => {
    statsSelectionRef.current = statsSelectionKey
  }, [statsSelectionKey])

  useEffect(() => {
    let cancelled = false
    void Promise.allSettled([fetchAccounts(false), fetchModels()]).then(([accountsResult, modelsResult]) => {
      if (cancelled) return
      if (accountsResult.status === 'fulfilled') setAccounts(accountsResult.value.data || [])
      if (modelsResult.status === 'fulfilled') setModelCount((modelsResult.value.data || []).length)
    })
      .finally(() => { if (!cancelled) setAccountsLoading(false) })
    return () => { cancelled = true }
  }, [])

  useEffect(() => {
    statsMounted.current = true
    return () => { statsMounted.current = false }
  }, [])

  const startStatsFetch = useCallback((key: string, query: { hours?: number; from?: string; to?: string }) => {
    if (inFlightStatsKeys.current.has(key)) return null
    inFlightStatsKeys.current.add(key)
    return fetchRequestStats(query).finally(() => { inFlightStatsKeys.current.delete(key) })
  }, [])

  const refreshStats = useCallback(async () => {
    const range = statsWindow === 'custom' ? {
      from: dateValueToISO(customRange?.start),
      to: dateValueToISO(customRange?.end, true),
    } : null
    const query = statsQuery(statsWindow, range)
    if (!query) return
    const key = statsSelectionKey
    const request = startStatsFetch(key, query)
    if (!request) return
    try {
      const data = await request
      if (!statsMounted.current || key !== statsSelectionRef.current) return
      setStats(data)
      setStatsError('')
    } catch (err) {
      if (!statsMounted.current || key !== statsSelectionRef.current) return
      setStatsError(err instanceof Error ? err.message : String(err))
    }
    if (statsMounted.current && key === statsSelectionRef.current) setStatsLoading(false)
  }, [customRange, startStatsFetch, statsSelectionKey, statsWindow])

  useEffect(() => {
    if (!statsRangeReady) return
    const range = statsWindow === 'custom' ? {
      from: dateValueToISO(customRange?.start),
      to: dateValueToISO(customRange?.end, true),
    } : null
    const query = statsQuery(statsWindow, range)
    if (!query) return
    const key = statsSelectionKey
    const request = startStatsFetch(key, query)
    if (!request) return
    void request
      .then((data) => {
        if (!statsMounted.current || key !== statsSelectionRef.current) return
        setStats(data)
        setStatsError('')
      })
      .catch((err) => {
        if (!statsMounted.current || key !== statsSelectionRef.current) return
        setStats(null)
        setStatsError(err instanceof Error ? err.message : String(err))
      })
      .finally(() => {
        if (statsMounted.current && key === statsSelectionRef.current) setStatsLoading(false)
      })
  }, [customRange, startStatsFetch, statsRangeReady, statsSelectionKey, statsWindow])

  useEffect(() => {
    if (statsAutoRefreshPaused || !statsRangeReady) return
    return startVisibleInterval(document, () => { void refreshStats() }, 30_000)
  }, [refreshStats, statsAutoRefreshPaused, statsRangeReady])

  // Resource usage polls on its own 5s cadence; independent of the stats window.
  useEffect(() => {
    let cancelled = false
    const tick = () => {
      void fetchSystemResources()
        .then((data) => { if (!cancelled) setResources(data) })
        .catch(() => { /* keep last good sample */ })
    }
    tick()
    const timer = setInterval(tick, 5000)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [])

  const metrics = [
    { label: t('metricRequests'), value: traffic.totals.requests as number | null, kind: 'compact' as const, detail: t('statsWindowHint', { window: t(STATS_WINDOW_LABEL[statsWindow]) }), ok: traffic.totals.requests > 0 },
    { label: t('metricSuccess'), value: traffic.totals.success_rate, kind: 'percent' as const, detail: `${traffic.totals.ok} ${t('logsFilterOk')} · ${traffic.totals.incomplete} ${t('logsFilterIncomplete')} · ${traffic.totals.error} ${t('logsFilterError')}`, ok: traffic.totals.requests === 0 || traffic.totals.success_rate >= 0.9 },
    { label: t('metricLatency'), value: traffic.latency.p95_ms ?? traffic.latency.avg_ms, kind: 'ms' as const, detail: `p50 ${formatLatency(traffic.latency.p50_ms)} · avg ${formatLatency(traffic.latency.avg_ms)}`, ok: traffic.latency.p95_ms == null || traffic.latency.p95_ms < 8000 },
    { label: t('metricTokens'), value: traffic.tokens.total, kind: 'compact' as const, detail: `${formatCompact(traffic.tokens.prompt)} / ${formatCompact(traffic.tokens.completion)}`, ok: true },
  ]

  if (loading && !overview) return <OverviewPageSkeleton />

  return (
    <div className="space-y-6">
      <section className="grid gap-4 border-b border-separator pb-4">
        <div className="min-w-0">
          <h2 data-gsap-reveal className="text-2xl font-semibold tracking-[-0.035em]">{t('homeDisplay')}</h2>
          <p className="mt-1 max-w-2xl text-sm leading-6 text-muted">{t('homeLead')}</p>
        </div>
        <div className="flex min-w-0 flex-wrap items-center gap-3">
          <FilterToggle
            className="max-w-full flex-wrap"
            value={statsWindow}
            onChange={(next) => {
              const value = next as StatsWindow
              setStatsWindow(value)
              const nextRange = value === 'custom' ? {
                from: dateValueToISO(customRange?.start),
                to: dateValueToISO(customRange?.end, true),
              } : null
              if (statsQuery(value, nextRange)) {
                setStatsLoading(true)
              } else {
                setStats(null)
                setStatsError('')
                setStatsLoading(false)
              }
            }}
            ariaLabel={t('statsTraffic')}
            options={[
              { id: 'today', label: t('statsWindowToday') },
              { id: '1', label: t('statsWindow1h') },
              { id: '24', label: t('statsWindow24h') },
              { id: '168', label: t('statsWindow7d') },
              { id: '720', label: t('statsWindow30d') },
              { id: 'custom', label: t('statsWindowCustom') },
            ]}
          />
          {statsWindow === 'custom' ? (
            <TimeRangePicker
              className="w-full min-w-0 sm:w-[22rem]"
              value={customRange}
              onChange={(next) => {
                setCustomRange(next)
                const nextRange = {
                  from: dateValueToISO(next?.start),
                  to: dateValueToISO(next?.end, true),
                }
                if (statsQuery('custom', nextRange)) {
                  setStatsLoading(true)
                } else {
                  setStats(null)
                  setStatsError('')
                  setStatsLoading(false)
                }
              }}
              ariaLabel={t('statsWindowCustom')}
              fromLabel={t('statsWindowFrom')}
              toLabel={t('statsWindowTo')}
            />
          ) : null}
          <Button
            size="sm"
            variant="secondary"
            aria-label={t(statsAutoRefreshPaused ? 'statsRefreshResume' : 'statsRefreshPause')}
            onPress={() => setStatsAutoRefreshPaused((paused) => !paused)}
          >
            {statsAutoRefreshPaused ? <Play size={14} /> : <Pause size={14} />}
            {t(statsAutoRefreshPaused ? 'statsRefreshResume' : 'statsRefreshPause')}
          </Button>
          <span className="text-[11px] text-muted">
            {t(statsAutoRefreshPaused ? 'statsRefreshPaused' : 'statsRefreshAuto')}
          </span>
          <div className="text-left sm:text-right">
            <div className="mono text-[11px] text-muted">{overview?.time || '—'}</div>
            <div className="mt-1 inline-flex items-center gap-2 text-sm font-medium">
              <span className="status-dot" data-state={proxyOk && workerOk ? 'ok' : 'danger'} />
              {proxyOk && workerOk ? t('running') : t('degraded')}
            </div>
          </div>
        </div>
      </section>

      {!statsRangeReady ? (
        <div className="rounded-2xl border border-border bg-surface-secondary px-5 py-3 text-sm text-muted">
          {t('statsWindowCompleteRangeHint')}
        </div>
      ) : null}

      {error ? <PageAlert title={t('failedOverview', { msg: error })} /> : null}

      <section data-gsap-reveal className="grid overflow-hidden rounded-3xl border border-border bg-surface sm:grid-cols-2 xl:grid-cols-4">
        {metrics.map((metric, index) => (
          <div
            key={metric.label}
            className={`min-h-32 p-5 ${index ? 'border-t border-separator sm:border-l sm:border-t-0' : ''} ${index === 2 ? 'sm:border-l-0 sm:border-t xl:border-l xl:border-t-0' : ''}`}
          >
            <div className="text-xs font-medium text-muted">{metric.label}</div>
            <div className="mt-6 flex items-end justify-between gap-3">
              <div>
                <div className="mono text-2xl font-semibold tracking-[-0.035em]">
                  {statsLoading && statsRangeReady ? <SkeletonBlock className="h-8 w-24" /> : !statsRangeReady || metric.value == null ? '—' : <CountUp value={metric.value} kind={metric.kind} />}
                </div>
                <div className="mono mt-1 text-[11px] text-muted">{statsLoading && statsRangeReady ? <SkeletonBlock className="h-3 w-28" /> : statsRangeReady ? metric.detail : '—'}</div>
              </div>
              {statsRangeReady ? <span className="status-dot" data-state={metric.ok ? 'ok' : 'danger'} /> : null}
            </div>
          </div>
        ))}
      </section>

      <section className="grid gap-5">
        <Card data-gsap-reveal className="overflow-hidden p-0">
          <div className="flex items-center justify-between gap-3 border-b border-separator px-5 py-4">
            <div>
              <h3 className="font-semibold tracking-[-0.015em]">{t('statsTraffic')}</h3>
              <p className="mt-0.5 text-xs text-muted">{t('statsTrafficHint')}</p>
            </div>
            <Chip size="sm" variant="soft" color={statsRangeReady && traffic.totals.error ? 'warning' : 'default'}>
              {statsRangeReady ? formatPercent(traffic.totals.success_rate) : '—'}
            </Chip>
          </div>
          <div className="space-y-5 p-5">
            {!statsRangeReady ? (
              <div className="flex min-h-72 items-center justify-center text-sm text-muted">—</div>
            ) : statsError ? (
              <div className="text-sm text-danger">{t('failedStats', { msg: statsError })}</div>
            ) : statsLoading ? (
              <TrafficChartSkeleton />
            ) : traffic.series.some((point) => point.requests > 0) ? (
              <Suspense fallback={<TrafficChartSkeleton />}>
                <TrafficChart
                  series={traffic.series}
                  lang={lang}
                  emptyLabel={t('statsEmptyTraffic')}
                  okLabel={t('logsFilterOk')}
                  errorLabel={t('logsFilterError')}
                />
              </Suspense>
            ) : (
              <TrafficChartEmpty emptyLabel={t('statsEmptyTraffic')} />
            )}
            <div className="grid grid-cols-2 gap-3 border-t border-separator pt-4 sm:grid-cols-4">
              {[
                [t('statsLatencyP50'), formatLatency(traffic.latency.p50_ms)],
                [t('statsLatencyP95'), formatLatency(traffic.latency.p95_ms)],
                [t('statsLatencyAvg'), formatLatency(traffic.latency.avg_ms)],
                [t('statsTTFB'), formatLatency(traffic.latency.ttfb_avg_ms)],
              ].map(([label, value]) => (
                <div key={String(label)}>
                  <div className="text-[11px] text-muted">{label}</div>
                  <div className="mono mt-1 text-sm font-medium">{statsLoading && statsRangeReady ? <SkeletonBlock className="h-4 w-16" /> : value}</div>
                </div>
              ))}
            </div>
          </div>
        </Card>
      </section>

      <section className="grid gap-5 xl:grid-cols-[minmax(0,1.15fr)_minmax(0,.85fr)]">
        <Card data-gsap-reveal className="overflow-hidden p-0">
          <div className="flex items-center justify-between gap-3 border-b border-separator px-5 py-4">
            <div>
              <h3 className="font-semibold tracking-[-0.015em]">{t('statsPool')}</h3>
              <p className="mt-0.5 text-xs text-muted">{t('statsPoolHint')}</p>
            </div>
            <Link to="/accounts" className="inline-flex items-center gap-1 text-xs font-medium text-muted hover:text-foreground">
              {t('navAccounts')}
              <ArrowUpRight size={12} />
            </Link>
          </div>
          <div className="grid grid-cols-3 divide-x divide-separator border-b border-separator">
            {[
              [t('ready'), readyAccounts, 'ok'],
              [t('hot'), hotAccounts, hotAccounts ? 'ok' : ''],
              [t('inFlight'), inFlight, inFlight ? 'ok' : ''],
            ].map(([label, value, state]) => (
              <div key={String(label)} className="px-4 py-3">
                <div className="text-[11px] text-muted">{label}</div>
                <div className="mt-1 flex items-center gap-2">
                  <span className="mono text-lg font-semibold">{value}</span>
                  {state ? <span className="status-dot" data-state={state} /> : null}
                </div>
              </div>
            ))}
          </div>
          <div className="divide-y divide-separator">
            {accountsLoading ? (
              <RankListSkeleton />
            ) : accounts.length === 0 ? (
              <div className="px-5 py-8 text-sm text-muted">{t('noAccounts')}</div>
            ) : accounts.slice(0, 6).map((account) => {
              const quota = account.quota
              const tone = quotaTone(quota?.percentage, quota?.exceeded)
              return (
                <div key={account.id} className="flex items-center gap-3 px-5 py-3">
                  <span className="status-dot shrink-0" data-state={account.ready ? 'ok' : 'danger'} />
                  <div className="min-w-0 flex-1">
                    <div className="flex min-w-0 items-center gap-2">
                      <ProviderMark provider={account.provider} size={14} />
                      <div className="truncate text-sm font-medium">{account.name || account.id}</div>
                    </div>
                    <div className="mono mt-0.5 text-[10px] text-muted">
                      {accountProviderLabel(account.provider, account.region, t)}
                      {' · '}
                      {account.hot ? t('hot') : account.ready ? t('ready') : t('degraded')}
                      {quota ? ` · ${Math.round(quota.percentage ?? 0)}%` : ''}
                    </div>
                  </div>
                  {quota ? (
                    <div className="w-16 shrink-0">
                      <div className="h-1 overflow-hidden rounded-[2px] bg-separator">
                        <div
                          className={`h-full ${tone === 'danger' ? 'bg-danger' : tone === 'warning' ? 'bg-warning' : 'bg-success'}`}
                          style={{ width: `${Math.min(100, Math.max(0, quota.percentage ?? 0))}%` }}
                        />
                      </div>
                    </div>
                  ) : (
                    <span className="mono text-[11px] text-muted">{account.in_flight ?? account.inFlight ?? 0}</span>
                  )}
                </div>
              )
            })}
          </div>
          <div className="flex items-center justify-between border-t border-separator px-5 py-3 text-[11px] text-muted">
            <span>{t('metricModels')} {modelCount}</span>
            <span className="flex min-w-0 flex-wrap items-center justify-end gap-x-2 gap-y-1">
              {['qoder', 'workbuddy', 'trae'].map((provider) => {
                const count = accounts.filter((account) => String(account.provider || 'qoder').toLowerCase() === provider).length
                if (!count) return null
                return (
                  <span key={provider} className="inline-flex items-center gap-1">
                    <ProviderMark provider={provider} size={11} />
                    {count}
                  </span>
                )
              })}
              <span>{coolingAccounts ? `${coolingAccounts} ${t('statsCooling')}` : `${accounts.length} ${t('accountCount')}`}</span>
            </span>
          </div>
        </Card>

        <ResourcesCard resources={resources} t={t} />
      </section>

      <section className="grid gap-5 xl:grid-cols-2 2xl:grid-cols-3">
        <Card data-gsap-reveal className="overflow-hidden p-0">
          <div className="flex items-center justify-between gap-3 border-b border-separator px-5 py-4">
            <div>
              <h3 className="font-semibold tracking-[-0.015em]">{t('statsTopProviders')}</h3>
              <p className="mt-0.5 text-xs text-muted">{t('statsTopProvidersHint')}</p>
            </div>
            <Pulse size={15} className="text-muted" />
          </div>
          {!statsRangeReady ? (
            <div className="px-5 py-8 text-sm text-muted">—</div>
          ) : statsLoading ? (
            <RankListSkeleton />
          ) : (
            <RankList
              empty={t('statsNoProviders')}
              items={(traffic.providers || []).map((item) => ({
                key: item.key,
                label: familyLabel(item.key, t),
                count: item.count,
                meta: `${item.ok}/${item.count}`,
                mark: item.key === '(unknown)' ? undefined : item.key,
              }))}
            />
          )}
        </Card>
        <Card data-gsap-reveal className="overflow-hidden p-0">
          <div className="flex items-center justify-between gap-3 border-b border-separator px-5 py-4">
            <div>
              <h3 className="font-semibold tracking-[-0.015em]">{t('statsTopModels')}</h3>
              <p className="mt-0.5 text-xs text-muted">{t('providerCatalog')}</p>
            </div>
            <Cube size={15} className="text-muted" />
          </div>
          {!statsRangeReady ? (
            <div className="px-5 py-8 text-sm text-muted">—</div>
          ) : statsLoading ? (
            <RankListSkeleton rows={4} />
          ) : (
            <RankList
              empty={t('statsNoModels')}
              items={traffic.models.map((item) => ({
                key: item.key,
                label: item.key === '(unknown)' ? t('statsUnknown') : item.key,
                count: item.count,
                meta: item.latency_avg_ms != null ? formatLatency(item.latency_avg_ms) : `${item.ok}/${item.count}`,
              }))}
            />
          )}
        </Card>

        <Card data-gsap-reveal className="overflow-hidden p-0">
          <div className="flex items-center justify-between gap-3 border-b border-separator px-5 py-4">
            <div>
              <h3 className="font-semibold tracking-[-0.015em]">{traffic.errors.length ? t('statsErrorMix') : t('statsTopAccounts')}</h3>
              <p className="mt-0.5 text-xs text-muted">{traffic.errors.length ? t('statsErrorMixHint') : t('statsPoolHint')}</p>
            </div>
            <Pulse size={15} className="text-muted" />
          </div>
          {!statsRangeReady ? (
            <div className="px-5 py-8 text-sm text-muted">—</div>
          ) : statsLoading ? (
            <RankListSkeleton rows={4} />
          ) : traffic.errors.length ? (
            <RankList
              empty={t('statsNoErrors')}
              danger
              items={traffic.errors.map((item) => ({
                key: item.key,
                label: errorLabel(item.key, t),
                count: item.count,
              }))}
            />
          ) : (
            <RankList
              empty={t('statsNoAccounts')}
              items={traffic.accounts.map((item) => ({
                key: item.key,
                label: accountLabel(accounts, item.key, t('statsUnassigned')),
                count: item.count,
                meta: `${item.ok}/${item.count}`,
              }))}
            />
          )}
        </Card>
      </section>
    </div>
  )
}

function resourcesTone(rssBytes: number, cpuPercent: number) {
  if (rssBytes >= 2 * 1024 * 1024 * 1024 || cpuPercent >= 200) return 'danger'
  if (rssBytes >= 1024 * 1024 * 1024 || cpuPercent >= 100) return 'warning'
  return 'ok'
}

function ResourcesCard({ resources, t }: { resources: SystemResources | null; t: (key: string, vars?: Record<string, string | number>) => string }) {
  const workers = [...(resources?.workers ?? [])].sort((a, b) => b.rss_bytes - a.rss_bytes)
  const server = resources?.server
  return (
    <Card data-gsap-reveal className="overflow-hidden p-0">
      <div className="flex items-center justify-between gap-3 border-b border-separator px-5 py-4">
        <div>
          <h3 className="font-semibold tracking-[-0.015em]">{t('resTitle')}</h3>
          <p className="mt-0.5 text-xs text-muted">{t('resHint')}</p>
        </div>
        <Chip size="sm" variant="soft" color={resources ? 'success' : 'default'}>
          {resources ? formatBytes(resources.total_rss_bytes) : '—'}
        </Chip>
      </div>
      <div className="divide-y divide-separator">
        {!resources ? (
          <RankListSkeleton rows={3} />
        ) : (
          <>
            <div className="px-5 py-3">
              <div className="flex items-baseline justify-between gap-3">
                <div className="flex min-w-0 items-center gap-2">
                  <span className="status-dot shrink-0" data-state={resourcesTone(server?.rss_bytes ?? 0, server?.cpu_percent ?? 0)} />
                  <div className="truncate text-sm font-medium">{t('resServer')}</div>
                  <span className="mono text-[10px] text-muted">pid {server?.pid}</span>
                </div>
                <div className="mono shrink-0 text-[11px] text-muted">
                  {formatBytes(server?.rss_bytes)} · {(server?.cpu_percent ?? 0).toFixed(1)}%
                </div>
              </div>
            </div>
            {workers.length === 0 ? (
              <div className="px-5 py-4 text-sm text-muted">{t('resEmpty')}</div>
            ) : workers.map((worker) => (
              <div key={worker.pid} className="px-5 py-3">
                <div className="flex items-baseline justify-between gap-3">
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="status-dot shrink-0" data-state={resourcesTone(worker.rss_bytes, worker.cpu_percent)} />
                    <div className="truncate text-sm font-medium">{worker.label || worker.account_id}</div>
                    <span className="mono text-[10px] text-muted">pid {worker.pid}</span>
                  </div>
                  <div className="mono shrink-0 text-[11px] text-muted">
                    {formatBytes(worker.rss_bytes)} · {worker.cpu_percent.toFixed(1)}%
                  </div>
                </div>
              </div>
            ))}
          </>
        )}
      </div>
      <div className="flex items-center justify-between border-t border-separator px-5 py-3 text-[11px] text-muted">
        <span className="mono">{server?.goroutines ?? 0} {t('resGoroutines')}</span>
        <span className="mono inline-flex items-center gap-1">
          <Cpu size={11} />
          {resources?.sampled_at ? new Date(resources.sampled_at).toLocaleTimeString() : '—'}
        </span>
      </div>
    </Card>
  )
}

function RankList({
  items,
  empty,
  danger,
}: {
  items: Array<{ key: string; label: string; count: number; meta?: string; mark?: string }>
  empty: string
  danger?: boolean
}) {
  const rootRef = useRef<HTMLDivElement>(null)
  const peak = Math.max(1, ...items.map((item) => item.count))
  const signature = items.map((item) => `${item.key}:${item.count}`).join('|')

  useLayoutEffect(() => {
    const root = rootRef.current
    if (!root) return
    const context = gsap.context(() => {
      const media = gsap.matchMedia()
      media.add('(prefers-reduced-motion: reduce)', () => {
        gsap.set('[data-rank-fill]', { clearProps: 'transform' })
      })
      media.add('(prefers-reduced-motion: no-preference)', () => {
        const fills = gsap.utils.toArray<HTMLElement>('[data-rank-fill]')
        if (!fills.length) return
        gsap.fromTo(fills, { scaleX: 0 }, {
          scaleX: 1,
          duration: 0.38,
          ease: 'power2.out',
          stagger: 0.05,
          transformOrigin: '0% 50%',
        })
      })
    }, root)
    return () => context.revert()
  }, [signature])

  if (!items.length) {
    return <div className="px-5 py-8 text-sm text-muted">{empty}</div>
  }
  return (
    <div ref={rootRef} className="divide-y divide-separator">
      {items.map((item) => (
        <div key={item.key} className="px-5 py-3">
          <div className="flex items-baseline justify-between gap-3">
            <div className="flex min-w-0 items-center gap-2">
              {item.mark ? <ProviderMark provider={item.mark} size={14} /> : null}
              <div className="truncate text-sm font-medium">{item.label}</div>
            </div>
            <div className="mono shrink-0 text-[11px] text-muted">
              {item.meta ? `${item.count} · ${item.meta}` : item.count}
            </div>
          </div>
          <div className="mt-2 h-1 overflow-hidden rounded-[2px] bg-separator">
            <div
              data-rank-fill
              className={`h-full origin-left rounded-[2px] ${danger ? 'bg-danger' : 'bg-success'}`}
              style={{ width: `${Math.max(6, (item.count / peak) * 100)}%` }}
            />
          </div>
        </div>
      ))}
    </div>
  )
}
