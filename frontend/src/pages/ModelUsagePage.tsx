import { useEffect, useMemo, useRef, useState } from 'react'
import { Button, Card, Chip, Drawer, Input, Label, Meter, Skeleton, Table } from '@heroui/react'
import { ArrowClockwise, ChartLineUp, MagnifyingGlass, PaperPlaneTilt, WarningCircle, X } from '@phosphor-icons/react'
import { Link } from 'react-router-dom'
import {
  fetchModelUsageDetail,
  fetchModelUsageDiagnostics,
  fetchModelUsageOverview,
  probeModel,
  type BudgetEstimate,
  type ModelUsageCompactQuery,
  type ModelUsageDetailResponse,
  type ModelUsageDiagnosticsResponse,
  type ModelUsageError,
  type ModelUsageOverviewResponse,
  type ModelUsageOverviewRow,
  type ModelUsageDetailRow,
  type RouteCandidate,
} from '@/api/model-usage'
import { fetchAccountUsage, updateAccountUsagePolicy, type AccountUsagePolicy } from '@/api/usage'
import { FilterSelect } from '@/components/ui/FilterSelect'
import { EmptyPanel } from '@/components/ui/EmptyPanel'
import { PageAlert } from '@/components/ui/PageAlert'
import { useI18n } from '@/hooks/useI18n'
import { budgetProgressExceedsReference, budgetProgressPercent, workbuddyBudgetKey } from '@/lib/model-usage'
import { canonicalUsageModelKey, hasModelUsageLimit, safeBudgetGuardSuggestion } from '@/lib/usagePolicy'

type WindowPreset = '24h' | '7d' | '30d'
type UsageView = 'overview' | 'diagnostics'
type BudgetWindow = 'reset' | '24h'

const EMPTY_MODEL_USAGE_ROWS: ModelUsageOverviewRow[] = []

function queryWindow(preset: WindowPreset) {
  const to = new Date()
  const from = new Date(to.getTime() - (preset === '24h' ? 24 : preset === '7d' ? 168 : 720) * 60 * 60 * 1000)
  return { from: from.toISOString(), to: to.toISOString() }
}

function formatCount(value?: number | null) {
  return value == null || !Number.isFinite(value) ? '—' : new Intl.NumberFormat().format(value)
}

function formatDate(value: string | null | undefined, lang: 'zh' | 'en') {
  if (!value) return '—'
  const date = new Date(value)
  if (!Number.isFinite(date.getTime())) return '—'
  return new Intl.DateTimeFormat(lang === 'zh' ? 'zh-CN' : 'en-US', {
    month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(date)
}

function usageCoverage(row: { known_usage_attempts: number; estimated_usage_attempts: number; missing_usage_attempts: number }) {
  const total = row.known_usage_attempts + row.estimated_usage_attempts + row.missing_usage_attempts
  return `${formatCount(row.known_usage_attempts)} / ${formatCount(total)}`
}

function errorSummary(item: ModelUsageError, t: (key: string) => string) {
  const code = item.code || ''
  if (code === 'application_shutdown') return t('modelUsageErrAppShutdown')
  if (code === '6004' && item.provider.toLowerCase() === 'workbuddy') return t('modelUsageErr6004')
  if (code === '3004' && item.provider.toLowerCase() === 'trae') return t('modelUsageErr3004')
  if (code === '4007' && item.provider.toLowerCase() === 'trae') return t('modelUsageErr4007')
  if (code === '4011' && item.provider.toLowerCase() === 'trae') return t('modelUsageErr4011')
  if (code === '4021') return t('modelUsageErrUnknown')
  if (code === '4022') return t('modelUsageErrContext')
  if (code === 'length') return t('modelUsageErrLength')
  if (item.kind === 'concurrency_capacity') return t('modelUsageErrConcurrencyCapacity')
  if (item.kind === 'auth') return t('modelUsageErrUpstreamAuth')
  if (item.kind === 'quota') return t('modelUsageErrCredits')
  if (item.kind === 'rate_limit') return t('modelUsageErrRateLimit')
  return t('modelUsageErrUnknown')
}

function errorSuggestion(item: ModelUsageError, t: (key: string) => string) {
  const code = item.code || ''
  if (code === 'application_shutdown') return t('modelUsageSuggestAppShutdown')
  if (code === '6004' && item.provider.toLowerCase() === 'workbuddy') return t('modelUsageSuggest6004')
  if ((code === '3004' || code === '4007' || code === '4011') && item.provider.toLowerCase() === 'trae') return t('modelUsageSuggestLimit')
  if (code === '4022') return t('modelUsageSuggestContext')
  if (code === 'length') return t('modelUsageSuggestLength')
  if (item.kind === 'concurrency_capacity') return t('modelUsageSuggestLocalCapacity')
  if (item.kind === 'auth') return t('modelUsageSuggestAuth')
  if (item.kind === 'quota') return t('modelUsageSuggestQuota')
  if (item.kind === 'rate_limit') return t('modelUsageSuggestLimit')
  return t('modelUsageSuggestUnknown')
}

function reasonLabel(reason: string, t: (key: string) => string) {
  const key = `modelUsageReason_${reason}`
  const label = t(key)
  return label === key ? t('modelUsageReason_unknown') : label
}

function budgetPathLabel(path: string | undefined, t: (key: string) => string) {
  switch (path) {
    case 'openai_chat_completions': return t('modelUsagePathChatCompletions')
    case 'openai_responses': return t('modelUsagePathResponses')
    case 'anthropic_messages': return t('modelUsagePathAnthropicMessages')
    default: return t('modelUsageRequestPathUnknown')
  }
}

function selectedBudget(row: ModelUsageDetailRow, selectedKey: string | undefined) {
  const candidates = row.budgets || []
  return candidates.find((item) => workbuddyBudgetKey(item) === selectedKey) || candidates[0]
}

export function ModelUsagePage() {
  const { t, lang } = useI18n()
  const [view, setView] = useState<UsageView>('overview')
  const [provider, setProvider] = useState('')
  const [region, setRegion] = useState('')
  const [activity, setActivity] = useState('all')
  const [preset, setPreset] = useState<WindowPreset>('24h')
  const [windowRange, setWindowRange] = useState(() => queryWindow('24h'))
  const [search, setSearch] = useState('')
  const [overviewPage, setOverviewPage] = useState(1)
  const [overview, setOverview] = useState<ModelUsageOverviewResponse | null>(null)
  const [overviewLoading, setOverviewLoading] = useState(true)
  const [overviewError, setOverviewError] = useState('')
  const [overviewEpoch, setOverviewEpoch] = useState(0)
  const [selectedModel, setSelectedModel] = useState<ModelUsageOverviewRow | null>(null)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [detailPage, setDetailPage] = useState(1)
  const [detailPageSize, setDetailPageSize] = useState(10)
  const [detailSort, setDetailSort] = useState<'account' | 'errors'>('account')
  const [detail, setDetail] = useState<ModelUsageDetailResponse | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState('')
  const [detailEpoch, setDetailEpoch] = useState(0)
  const [budgetWindow, setBudgetWindow] = useState<BudgetWindow>('reset')
  const [budgetVariantByAccount, setBudgetVariantByAccount] = useState<Record<string, string>>({})
  const [diagnosticModel, setDiagnosticModel] = useState('')
  const [diagnosticAccount, setDiagnosticAccount] = useState('')
  const [diagnosticPage, setDiagnosticPage] = useState(1)
  const [diagnostics, setDiagnostics] = useState<ModelUsageDiagnosticsResponse | null>(null)
  const [diagnosticsLoading, setDiagnosticsLoading] = useState(false)
  const [diagnosticsError, setDiagnosticsError] = useState('')
  const [diagnosticsEpoch, setDiagnosticsEpoch] = useState(0)
  const [probeBusy, setProbeBusy] = useState(false)
  const [probeResult, setProbeResult] = useState('')
  const [probeError, setProbeError] = useState('')
  const overviewRef = useRef<HTMLDivElement>(null)
  const savedScrollTop = useRef(0)
  const probeController = useRef<AbortController | null>(null)
  const rows = overview?.data.rows ?? EMPTY_MODEL_USAGE_ROWS
  const currentDiagnosticRow = rows.find((row) => JSON.stringify([row.provider, row.region, row.model]) === diagnosticModel)
    || (selectedModel && JSON.stringify([selectedModel.provider, selectedModel.region, selectedModel.model]) === diagnosticModel ? selectedModel : undefined)
  const currentDiagnosticProvider = currentDiagnosticRow?.provider || provider
  const currentDiagnosticRegion = currentDiagnosticRow?.region || region

  useEffect(() => {
    const controller = new AbortController()
    const timer = window.setTimeout(() => {
      setOverviewLoading(true)
      setOverviewError('')
      const query: ModelUsageCompactQuery = {
        provider, region, activity, search, page: overviewPage, page_size: 20, ...windowRange,
      }
      void fetchModelUsageOverview(query, controller.signal)
        .then((result) => setOverview(result))
        .catch((reason: unknown) => {
          if (!controller.signal.aborted) setOverviewError(reason instanceof Error ? reason.message : String(reason))
        })
        .finally(() => { if (!controller.signal.aborted) setOverviewLoading(false) })
    }, 120)
    return () => { window.clearTimeout(timer); controller.abort() }
  }, [provider, region, activity, search, overviewPage, windowRange, overviewEpoch])

  useEffect(() => {
    if (!drawerOpen || !selectedModel) return
    const controller = new AbortController()
    setDetailLoading(true)
    setDetailError('')
    const query: ModelUsageCompactQuery = {
      provider: selectedModel.provider, region: selectedModel.region, model: selectedModel.model,
      page: detailPage, page_size: detailPageSize, sort: detailSort, ...windowRange,
    }
    void fetchModelUsageDetail(query, controller.signal)
      .then((result) => setDetail(result))
      .catch((reason: unknown) => {
        if (!controller.signal.aborted) setDetailError(reason instanceof Error ? reason.message : String(reason))
      })
      .finally(() => { if (!controller.signal.aborted) setDetailLoading(false) })
    return () => controller.abort()
  }, [drawerOpen, selectedModel, detailPage, detailPageSize, detailSort, windowRange, detailEpoch])

  useEffect(() => {
    if (view !== 'diagnostics') return
    const controller = new AbortController()
    setDiagnosticsLoading(true)
    setDiagnosticsError('')
    const query: ModelUsageCompactQuery = {
      provider: currentDiagnosticRow?.provider || provider,
      region: currentDiagnosticRow?.region || region,
      model: currentDiagnosticRow?.model || '', account: diagnosticAccount,
      page: diagnosticPage, page_size: 20, ...windowRange,
    }
    void fetchModelUsageDiagnostics(query, controller.signal)
      .then((result) => setDiagnostics(result))
      .catch((reason: unknown) => {
        if (!controller.signal.aborted) setDiagnosticsError(reason instanceof Error ? reason.message : String(reason))
      })
      .finally(() => { if (!controller.signal.aborted) setDiagnosticsLoading(false) })
    return () => controller.abort()
  }, [view, provider, region, currentDiagnosticRow?.provider, currentDiagnosticRow?.region, currentDiagnosticRow?.model,
    diagnosticModel, diagnosticAccount, diagnosticPage, windowRange, diagnosticsEpoch])

  useEffect(() => () => probeController.current?.abort(), [])

  const modelOptions = useMemo(() => {
    const available = [...rows]
    if (selectedModel && !available.some((row) => row.provider === selectedModel.provider && row.region === selectedModel.region && row.model === selectedModel.model)) available.push(selectedModel)
    return available.map((row) => ({
      id: JSON.stringify([row.provider, row.region, row.model]),
      label: `${row.model} · ${row.provider}/${row.region}`,
    }))
  }, [rows, selectedModel])
  const routeOptions = useMemo(() => (diagnostics?.routes || []).map((item) => ({
    id: item.id, label: `${item.id} · ${reasonLabel(item.reason, t)}`,
  })), [diagnostics, t])
  const overviewPages = Math.max(1, Math.ceil((overview?.data.total || 0) / 20))
  const detailPages = Math.max(1, Math.ceil((detail?.data.total || 0) / detailPageSize))
  const diagnosticPages = Math.max(1, Math.ceil((diagnostics?.data.total || 0) / 20))

  function resetOverviewFilters() {
    setOverviewPage(1)
    setSelectedModel(null)
    setDrawerOpen(false)
    setDiagnosticModel('')
    setDiagnosticAccount('')
    setDiagnosticPage(1)
    setDiagnostics(null)
  }

  function openModel(row: ModelUsageOverviewRow) {
    savedScrollTop.current = window.scrollY
    setSelectedModel(row)
    setDetailPage(1)
    setDetailSort('account')
    setDetail(null)
    setDetailError('')
    setBudgetVariantByAccount({})
    setDrawerOpen(true)
    setDiagnosticModel(JSON.stringify([row.provider, row.region, row.model]))
    setDiagnosticAccount('')
    setDiagnosticPage(1)
  }

  function closeDrawer() {
    setDrawerOpen(false)
    window.requestAnimationFrame(() => window.scrollTo({ top: savedScrollTop.current, behavior: 'auto' }))
  }

  function selectDiagnosticModel(value: string) {
    setDiagnosticModel(value)
    setDiagnosticAccount('')
    setDiagnosticPage(1)
    setDiagnostics(null)
  }

  async function runProbe() {
    const row = rows.find((item) => JSON.stringify([item.provider, item.region, item.model]) === diagnosticModel)
      || (selectedModel && JSON.stringify([selectedModel.provider, selectedModel.region, selectedModel.model]) === diagnosticModel ? selectedModel : undefined)
    if (!row || !diagnosticAccount || probeBusy) return
    const controller = new AbortController()
    probeController.current = controller
    setProbeBusy(true)
    setProbeResult('')
    setProbeError('')
    try {
      const result = await probeModel(diagnosticAccount, row.model, controller.signal)
      setProbeResult(`${result.request_id || '—'} · ${result.observed_account_id || '—'} · ${result.finish_reason || '—'} (${result.finish_reason_source || 'unknown'}) · max_tokens ${result.requested_max_tokens} · usage ${result.prompt_tokens ?? '—'}/${result.completion_tokens ?? '—'} (${result.usage_source || 'unknown'}) · ${result.output || ''}`)
      if (!result.ok) setProbeError(result.error_message || result.error_code || t('modelUsageProbeFailed'))
      setWindowRange(queryWindow(preset))
      setOverviewEpoch((value) => value + 1)
      setDiagnosticsEpoch((value) => value + 1)
      setDetailEpoch((value) => value + 1)
    } catch (reason) {
      if (!(reason instanceof DOMException && reason.name === 'AbortError')) setProbeError(reason instanceof Error ? reason.message : String(reason))
    } finally {
      probeController.current = null
      setProbeBusy(false)
    }
  }

  const providerOptions = [
    { id: '', label: t('modelUsageAll') },
    { id: 'workbuddy', label: t('providerWorkBuddy') },
    { id: 'trae', label: t('providerTrae') },
  ]
  const regionOptions = [
    { id: '', label: t('modelUsageAll') },
    { id: 'cn', label: t('modelUsageRegionCn') },
    { id: 'global', label: t('modelUsageRegionGlobal') },
    { id: 'unknown', label: t('modelUsageUnknown') },
  ]
  const activityOptions = [
    { id: 'all', label: t('modelUsageActivityAll') },
    { id: 'used', label: t('modelUsageActivityUsed') },
    { id: 'errors', label: t('modelUsageActivityErrors') },
    { id: 'unused', label: t('modelUsageActivityUnused') },
  ]
  const presetOptions = [
    { id: '24h', label: t('modelUsage24h') },
    { id: '7d', label: t('modelUsage7d') },
    { id: '30d', label: t('modelUsage30d') },
  ]

  return (
    <div ref={overviewRef} className="mx-auto w-full max-w-[1440px] space-y-4 px-4 pb-10 pt-5 md:px-8">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2 text-muted"><ChartLineUp size={17} /><span className="text-xs font-semibold uppercase tracking-[0.16em]">{t('modelUsageEyebrow')}</span></div>
          <h1 className="mt-1.5 text-2xl font-semibold tracking-[-0.035em]">{t('modelUsageTitle')}</h1>
          <p className="mt-1 max-w-3xl text-sm leading-5 text-muted">{t('modelUsageIntro')}</p>
        </div>
        <Button variant="secondary" isDisabled={overviewLoading} onPress={() => { setWindowRange(queryWindow(preset)); setOverviewEpoch((value) => value + 1) }}><ArrowClockwise size={15} />{t('refresh')}</Button>
      </div>

      <div className="flex flex-wrap gap-2" role="group" aria-label={t('modelUsageTitle')}>
        <Button size="sm" variant={view === 'overview' ? 'primary' : 'secondary'} aria-pressed={view === 'overview'} onPress={() => setView('overview')}>
          <ChartLineUp size={15} />{t('modelUsageOverviewTab')}
        </Button>
        <Button size="sm" variant={view === 'diagnostics' ? 'primary' : 'secondary'} aria-pressed={view === 'diagnostics'} onPress={() => setView('diagnostics')}>
          <WarningCircle size={15} />{t('modelUsageDiagnosticsTab')}
        </Button>
      </div>

      <Card>
        <Card.Content className="grid grid-cols-1 gap-2.5 p-3 sm:grid-cols-2 xl:grid-cols-[1.1fr_0.8fr_0.9fr_0.9fr_1.1fr]">
          <FilterSelect value={provider} onChange={(value) => { resetOverviewFilters(); setProvider(value); setDiagnosticPage(1) }} ariaLabel={t('modelUsageProvider')} options={providerOptions} width="full" />
          <FilterSelect value={region} onChange={(value) => { resetOverviewFilters(); setRegion(value); setDiagnosticPage(1) }} ariaLabel={t('modelUsageRegion')} options={regionOptions} width="full" />
          <FilterSelect value={activity} onChange={(value) => { resetOverviewFilters(); setActivity(value || 'all') }} ariaLabel={t('modelUsageActivity')} options={activityOptions} width="full" />
          <FilterSelect value={preset} onChange={(value) => { if (value === '24h' || value === '7d' || value === '30d') { resetOverviewFilters(); setPreset(value); setWindowRange(queryWindow(value)); setDiagnosticPage(1) } }} ariaLabel={t('modelUsageWindow')} options={presetOptions} width="full" />
          <div className="relative min-w-0">
            <MagnifyingGlass size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-muted" />
            <Input aria-label={t('modelUsageSearch')} placeholder={t('modelUsageSearch')} value={search} onChange={(event) => { resetOverviewFilters(); setSearch(event.target.value) }} className="w-full pl-9" />
          </div>
          <span className="text-xs text-muted sm:col-span-2 xl:col-span-5">{t('modelUsageOverviewHint')} · {overview ? `${formatDate(overview.data.from, lang)} – ${formatDate(overview.data.to, lang)}` : t('modelUsageLocalOnly')}</span>
        </Card.Content>
      </Card>

      {overviewError ? <PageAlert title={t('modelUsageLoadFailed', { msg: overviewError })} description={t('modelUsageOverviewRetry')} /> : null}

      <section className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4" aria-busy={overviewLoading}>
        <SummaryCard title={t('modelUsageSummaryRequests')} value={overviewLoading && !overview ? '…' : formatCount(overview?.data.summary.requests)} hint={t('modelUsageRequestsDeduplicated')} />
        <SummaryCard title={t('modelUsageAttempts')} value={overviewLoading && !overview ? '…' : formatCount(overview?.data.summary.attempts)} hint={t('modelUsageAttemptsHint')} />
        <SummaryCard title={t('modelUsageKnownTokens')} value={overviewLoading && !overview ? '…' : formatCount((overview?.data.summary.prompt_tokens || 0) + (overview?.data.summary.completion_tokens || 0))} hint={`${t('modelUsagePrompt')}: ${formatCount(overview?.data.summary.prompt_tokens)} · ${t('modelUsageCompletion')}: ${formatCount(overview?.data.summary.completion_tokens)}`} />
        <SummaryCard title={t('modelUsageUsageCoverage')} value={overviewLoading && !overview ? '…' : `${formatCount(overview?.data.summary.known_usage_attempts)} / ${formatCount((overview?.data.summary.known_usage_attempts || 0) + (overview?.data.summary.estimated_usage_attempts || 0) + (overview?.data.summary.missing_usage_attempts || 0))}`} hint={t('modelUsageUnknownNotZero')} />
      </section>

      {overview?.data.catalog_limited ? <PageAlert status="warning" title={t('modelUsageCatalogLimited')} /> : null}

      {view === 'overview' ? (
        <section className="space-y-3" aria-busy={overviewLoading}>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="text-xs text-muted">{overview ? t('modelUsagePageOf', { page: overview.data.page, pages: overviewPages, total: overview.data.total }) : '—'}</div>
            <Pager page={overviewPage} pages={overviewPages} hasMore={Boolean(overview?.data.has_more)} loading={overviewLoading} onPage={setOverviewPage} previous={t('modelUsagePrevious')} next={t('modelUsageNext')} />
          </div>
          <Card className="overflow-hidden p-0">
            {overviewLoading && !overview ? <OverviewSkeleton /> : rows.length === 0 ? <div className="p-5"><EmptyPanel icon={<ChartLineUp size={22} />} title={search || activity !== 'all' ? t('modelUsageNoMatches') : t('modelUsageNoRows')} /></div> : (
              <>
                <div className="hidden md:block">
                  <Table>
                    <Table.ScrollContainer className="min-w-0 overflow-x-auto">
                      <Table.Content aria-label={t('modelUsageOverviewTab')} className="min-w-[760px]">
                        <Table.Header>
                          <Table.Column isRowHeader>{t('modelUsageColModel')}</Table.Column>
                          <Table.Column>{t('modelUsageSummaryRequests')}</Table.Column>
                          <Table.Column>{t('modelUsageKnownTokens')}</Table.Column>
                          <Table.Column>{t('modelUsageAccountsAvailable')}</Table.Column>
                          <Table.Column>{t('modelUsageAccountsRestricted')}</Table.Column>
                          <Table.Column>{t('modelUsageModelDetails')}</Table.Column>
                        </Table.Header>
                        <Table.Body>
                          {rows.map((row) => <Table.Row key={`${row.provider}/${row.region}/${row.model}`}>
                            <Table.Cell><ModelIdentity row={row} lang={lang} t={t} /></Table.Cell>
                            <Table.Cell><div className="mono text-sm">{formatCount(row.requests)}</div><div className="mt-0.5 text-[10px] text-muted">{t('modelUsageAttempts')}: {formatCount(row.attempts)}</div></Table.Cell>
                            <Table.Cell><TokenCell row={row} t={t} /></Table.Cell>
                            <Table.Cell><span className="mono">{formatCount(row.schedulable_accounts)}</span></Table.Cell>
                            <Table.Cell><span className="mono">{formatCount(row.restricted_accounts)}</span></Table.Cell>
                            <Table.Cell><Button size="sm" variant="secondary" onPress={() => openModel(row)}>{t('modelUsageModelDetails')}</Button></Table.Cell>
                          </Table.Row>)}
                        </Table.Body>
                      </Table.Content>
                    </Table.ScrollContainer>
                  </Table>
                </div>
                <div className="divide-y divide-separator md:hidden">
                  {rows.map((row) => <article key={`${row.provider}/${row.region}/${row.model}`} className="space-y-2.5 p-3">
                    <div className="flex items-start justify-between gap-3"><ModelIdentity row={row} lang={lang} t={t} /><Button size="sm" variant="secondary" onPress={() => openModel(row)}>{t('modelUsageModelDetails')}</Button></div>
                    <div className="grid grid-cols-2 gap-x-3 gap-y-2 border-t border-separator pt-2 text-xs">
                      <div><span className="text-muted">{t('modelUsageSummaryRequests')}</span><div className="mono mt-0.5">{formatCount(row.requests)} <span className="text-muted">/ {formatCount(row.attempts)} {t('modelUsageAttempts')}</span></div></div>
                      <div><span className="text-muted">{t('modelUsageKnownTokens')}</span><TokenCell row={row} t={t} compact /></div>
                      <div><span className="text-muted">{t('modelUsageAccountsAvailable')}</span><div className="mono mt-0.5">{formatCount(row.schedulable_accounts)}</div></div>
                      <div><span className="text-muted">{t('modelUsageAccountsRestricted')}</span><div className="mono mt-0.5">{formatCount(row.restricted_accounts)}</div></div>
                    </div>
                  </article>)}
                </div>
              </>
            )}
          </Card>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="text-xs text-muted">{overview ? t('modelUsagePageOf', { page: overview.data.page, pages: overviewPages, total: overview.data.total }) : '—'}</div>
            <Pager page={overviewPage} pages={overviewPages} hasMore={Boolean(overview?.data.has_more)} loading={overviewLoading} onPage={setOverviewPage} previous={t('modelUsagePrevious')} next={t('modelUsageNext')} />
          </div>
        </section>
      ) : (
        <DiagnosticsPanel
          t={t}
          lang={lang}
          rows={rows}
          modelValue={diagnosticModel}
          accountValue={diagnosticAccount}
          modelOptions={modelOptions}
          routeOptions={routeOptions}
          diagnostics={diagnostics}
          loading={diagnosticsLoading}
          error={diagnosticsError}
          page={diagnosticPage}
          pages={diagnosticPages}
          onModel={selectDiagnosticModel}
          onAccount={(value) => { setDiagnosticAccount(value); setDiagnosticPage(1); setDiagnostics(null) }}
          onPage={setDiagnosticPage}
          onRetry={() => { setWindowRange(queryWindow(preset)); setDiagnosticsEpoch((value) => value + 1) }}
          provider={currentDiagnosticProvider}
          region={currentDiagnosticRegion}
          probeBusy={probeBusy}
          probeResult={probeResult}
          probeError={probeError}
          onProbe={() => void runProbe()}
          onCancelProbe={() => probeController.current?.abort()}
        />
      )}

      <Drawer isOpen={drawerOpen} onOpenChange={(open) => { if (!open) closeDrawer(); else setDrawerOpen(true) }}>
        <Drawer.Content placement="right">
          <Drawer.Dialog className="flex h-dvh w-full max-w-[100vw] flex-col bg-surface text-foreground sm:max-w-[min(100vw,68rem)]">
            <Drawer.Header className="flex flex-row shrink-0 items-start justify-between gap-4 border-b border-separator px-4 py-4 sm:px-6">
              <div className="min-w-0 flex-1">
                <Drawer.Heading className="truncate text-lg font-semibold">{selectedModel?.model || t('modelUsageDetailTitle')}</Drawer.Heading>
                <p className="mono mt-1 text-xs text-muted">{selectedModel?.provider}/{selectedModel?.region} · {t('modelUsageRecent')} · {selectedModel ? formatDate(selectedModel.last_activity_at, lang) : '—'}</p>
              </div>
              <Button className="shrink-0" isIconOnly size="sm" variant="ghost" aria-label={t('close')} onPress={closeDrawer}><X size={17} /></Button>
            </Drawer.Header>
            <Drawer.Body className="min-h-0 flex-1 overflow-y-auto px-4 py-4 sm:px-6">
              {detailError ? <div className="mb-3"><PageAlert title={t('modelUsageLoadFailed', { msg: detailError })} description={t('modelUsageDetailRetry')} /></div> : null}
              <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
                <p className="max-w-3xl text-xs text-muted">{t('modelUsageDetailHint')}</p>
                <Button size="sm" variant="secondary" isDisabled={detailLoading} onPress={() => { setWindowRange(queryWindow(preset)); setDetailEpoch((value) => value + 1) }}><ArrowClockwise size={14} />{t('refresh')}</Button>
              </div>
              <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
                <div className="flex flex-wrap gap-2">
                  <Button size="sm" variant={detailSort === 'account' ? 'primary' : 'secondary'} onPress={() => { setDetailSort('account'); setDetailPage(1) }}>{t('modelUsageSortAccount')}</Button>
                  <Button size="sm" variant={detailSort === 'errors' ? 'primary' : 'secondary'} onPress={() => { setDetailSort('errors'); setDetailPage(1) }}>{t('modelUsageSortErrors')}</Button>
                </div>
                <div className="flex items-center gap-2">
                  <Label className="text-xs text-muted">{t('modelUsagePageSize')}</Label>
                  <FilterSelect value={String(detailPageSize)} onChange={(value) => { const size = Number(value); if (size === 10 || size === 20) { setDetailPageSize(size); setDetailPage(1) } }} ariaLabel={t('modelUsagePageSize')} options={[{ id: '10', label: '10' }, { id: '20', label: '20' }]} />
                </div>
              </div>
              <div className="mb-2 flex flex-wrap items-center justify-between gap-3">
                <div className="text-xs text-muted">{detail ? t('modelUsagePageAccounts', { page: detailPage, pages: detailPages, total: detail.data.total }) : '—'}</div>
                <div className="flex gap-1 rounded-xl border border-separator p-1">
                  <Button size="sm" variant={budgetWindow === 'reset' ? 'primary' : 'ghost'} onPress={() => setBudgetWindow('reset')}>{t('modelUsageBudgetWindowReset')}</Button>
                  <Button size="sm" variant={budgetWindow === '24h' ? 'primary' : 'ghost'} onPress={() => setBudgetWindow('24h')}>{t('modelUsageBudgetWindow24h')}</Button>
                </div>
              </div>
              {detailLoading && !detail ? <DetailSkeleton /> : detail?.data.rows.length ? (
                <>
                  <div className="hidden lg:block">
                    <Table>
                      <Table.ScrollContainer className="min-w-0 overflow-x-auto">
                        <Table.Content aria-label={t('modelUsageDetailTitle')} className="min-w-[820px]">
                          <Table.Header><Table.Column isRowHeader>{t('modelUsageColAccount')}</Table.Column><Table.Column>{t('modelUsageAccountState')}</Table.Column><Table.Column>{t('modelUsageColCounts')}</Table.Column><Table.Column>{t('modelUsageColTokens')}</Table.Column><Table.Column>{t('modelUsageBudget')}</Table.Column><Table.Column>{t('modelUsageBudgetEvidence')}</Table.Column></Table.Header>
                          <Table.Body>{detail.data.rows.map((row) => <Table.Row key={row.account.id}>
                            <Table.Cell><AccountIdentity item={row} lang={lang} t={t} /></Table.Cell>
                            <Table.Cell><AccountState item={row} t={t} lang={lang} /></Table.Cell>
                            <Table.Cell><div className="text-xs">{t('modelUsageRequests')}: {formatCount(row.usage.requests)}</div><div className="mt-1 text-[10px] text-muted">{t('modelUsageAttempts')}: {formatCount(row.usage.attempts)}</div></Table.Cell>
                            <Table.Cell><div className="mono text-xs">{formatCount(row.usage.prompt_tokens)} / {formatCount(row.usage.completion_tokens)}</div><div className="mt-1 text-[10px] text-muted">{t('modelUsagePrompt')} / {t('modelUsageCompletion')} · {usageCoverage(row.usage)}</div></Table.Cell>
                            <Table.Cell><SelectedBudget item={row} window={budgetWindow} selectedKey={budgetVariantByAccount[row.account.id]} lang={lang} t={t} /></Table.Cell>
                          <Table.Cell><BudgetEvidence row={row} selectedKey={budgetVariantByAccount[row.account.id]} lang={lang} t={t} onVariant={(key) => setBudgetVariantByAccount((prev) => ({ ...prev, [row.account.id]: key }))} /></Table.Cell>
                          </Table.Row>)}</Table.Body>
                        </Table.Content>
                      </Table.ScrollContainer>
                    </Table>
                  </div>
                  <div className="space-y-2 lg:hidden">
                    {detail.data.rows.map((row) => <Card key={row.account.id} className="overflow-hidden p-0">
                      <Card.Content className="space-y-3 p-3">
                        <div className="flex flex-wrap items-start justify-between gap-2"><AccountIdentity item={row} lang={lang} t={t} /><AccountState item={row} t={t} lang={lang} /></div>
                        <div className="grid grid-cols-2 gap-3 border-t border-separator pt-2 text-xs"><div>{t('modelUsageRequests')}: <span className="mono">{formatCount(row.usage.requests)}</span><div className="mt-1 text-[10px] text-muted">{t('modelUsageAttempts')}: {formatCount(row.usage.attempts)}</div></div><div>{t('modelUsagePrompt')} / {t('modelUsageCompletion')}<div className="mono mt-1">{formatCount(row.usage.prompt_tokens)} / {formatCount(row.usage.completion_tokens)}</div><div className="mt-1 text-[10px] text-muted">{usageCoverage(row.usage)}</div></div></div>
                        <SelectedBudget item={row} window={budgetWindow} selectedKey={budgetVariantByAccount[row.account.id]} lang={lang} t={t} />
                        <BudgetEvidence row={row} selectedKey={budgetVariantByAccount[row.account.id]} lang={lang} t={t} onVariant={(key) => setBudgetVariantByAccount((prev) => ({ ...prev, [row.account.id]: key }))} />
                      </Card.Content>
                    </Card>)}
                  </div>
                  <Pager page={detailPage} pages={detailPages} hasMore={detail.data.has_more} loading={detailLoading} onPage={setDetailPage} previous={t('modelUsagePrevious')} next={t('modelUsageNext')} />
                </>
              ) : detailLoading ? <DetailSkeleton /> : <EmptyPanel icon={<ChartLineUp size={22} />} title={t('modelUsageNoAccounts')} />}
              {detail?.data.budget_observations_truncated ? <PageAlert status="warning" title={t('modelUsageBudgetTruncated')} /> : null}
              {detail?.data.catalog_limited ? <PageAlert status="warning" title={t('modelUsageCatalogLimited')} /> : null}
            </Drawer.Body>
          </Drawer.Dialog>
        </Drawer.Content>
      </Drawer>
    </div>
  )
}

function SummaryCard({ title, value, hint }: { title: string; value: string; hint: string }) {
  return <Card><Card.Content className="p-3.5"><div className="text-xs text-muted">{title}</div><div className="mono mt-1.5 text-xl font-semibold">{value}</div><div className="mt-1 text-[10px] leading-4 text-muted">{hint}</div></Card.Content></Card>
}

function ModelIdentity({ row, lang, t }: { row: ModelUsageOverviewRow; lang: 'zh' | 'en'; t: (key: string) => string }) {
  return <div className="min-w-0">
    <div className="truncate text-sm font-semibold">{row.model}</div>
    <div className="mt-1 flex flex-wrap items-center gap-1.5 text-[10px] text-muted"><span>{row.provider}/{row.region}</span><Chip size="sm" variant="soft" color={row.recent_usage ? 'success' : 'default'}>{row.recent_usage ? t('modelUsageRecent') : t('modelUsageNoRecent')}</Chip>{row.last_activity_at ? <span>{formatDate(row.last_activity_at, lang)}</span> : null}</div>
  </div>
}

function TokenCell({ row, t, compact = false }: { row: ModelUsageOverviewRow; t: (key: string) => string; compact?: boolean }) {
  if (!row.recent_usage && row.prompt_tokens == null && row.completion_tokens == null) return <div className="mt-0.5 text-xs text-muted">{t('modelUsageNoRecent')}</div>
  return <div className={`mono ${compact ? 'mt-0.5 text-xs' : 'text-xs'}`}>{formatCount(row.prompt_tokens)} / {formatCount(row.completion_tokens)}<div className="mt-1 text-[10px] text-muted">{usageCoverage(row)}</div></div>
}

function Pager({ page, pages, hasMore, loading, onPage, previous, next }: { page: number; pages: number; hasMore: boolean; loading: boolean; onPage: (value: number) => void; previous: string; next: string }) {
  if (pages <= 1 && page <= 1 && !hasMore) return null
  return <div className="flex items-center gap-2">
    <Button size="sm" variant="secondary" isDisabled={loading || page <= 1} onPress={() => onPage(Math.max(1, page - 1))}>{previous}</Button>
    <span className="mono min-w-10 text-center text-xs text-muted">{page} / {pages}</span>
    <Button size="sm" variant="secondary" isDisabled={loading || (!hasMore && page >= pages)} onPress={() => onPage(Math.min(pages, page + 1))}>{next}</Button>
  </div>
}

function OverviewSkeleton() {
  return <div className="space-y-3 p-4">{Array.from({ length: 8 }, (_, index) => <div key={index} className="flex items-center gap-4 rounded-lg border border-separator p-3"><Skeleton className="h-4 w-48 rounded-lg" /><Skeleton className="h-4 w-20 rounded-lg" /><Skeleton className="h-4 w-32 rounded-lg" /><Skeleton className="h-8 w-24 rounded-lg" /></div>)}</div>
}

function DetailSkeleton() {
  return <div className="space-y-2">{Array.from({ length: 6 }, (_, index) => <div key={index} className="grid grid-cols-2 gap-3 rounded-xl border border-separator p-3 sm:grid-cols-5"><Skeleton className="h-4 w-28 rounded-lg" /><Skeleton className="h-4 w-20 rounded-lg" /><Skeleton className="h-4 w-24 rounded-lg" /><Skeleton className="h-4 w-32 rounded-lg" /><Skeleton className="h-4 w-16 rounded-lg" /></div>)}</div>
}

function AccountIdentity({ item, lang, t }: { item: ModelUsageDetailRow; lang: 'zh' | 'en'; t: (key: string) => string }) {
  return <div className="min-w-0"><div className="max-w-56 truncate text-xs font-medium">{item.account.name || item.account.id}</div><div className="mono mt-1 max-w-56 truncate text-[10px] text-muted">{item.account.id} · {item.account.provider}/{item.account.region}</div><div className="mt-1 text-[10px] text-muted">{t('modelUsageLastSuccess')}: {formatDate(item.usage.last_success_at, lang)}</div></div>
}

function AccountState({ item, t, lang }: { item: ModelUsageDetailRow; t: (key: string) => string; lang: 'zh' | 'en' }) {
  let label = t('modelUsageModelUnavailable')
  let color: 'success' | 'warning' | 'danger' | 'default' = 'default'
  const candidate = item.route
  if (!item.account.enabled) { label = t('modelUsageDisabledState'); color = 'default' }
  else if (!item.account.ready) { label = t('modelUsageNeedsLoginState'); color = 'warning' }
  else if (candidate?.schedulable) { label = t('modelUsageAvailable'); color = 'success' }
  else if (candidate) { label = `${t('modelUsageRestrictedState')} · ${reasonLabel(candidate.reason, t)}`; color = 'warning' }
  const recovery = candidate?.model_cooldown_until || candidate?.account_cooldown_until || item.account.down_until
  return <div className="min-w-0"><Chip size="sm" variant="soft" color={color}>{label}</Chip>{recovery ? <div className="mt-1 text-[10px] text-muted">{t('modelUsageCoolingUntil')}: {formatDate(recovery, lang)}</div> : candidate && !candidate.schedulable ? <div className="mt-1 text-[10px] text-muted">{t('modelUsageUnknownRecovery')}</div> : null}</div>
}

function SelectedBudget({ item, window, selectedKey, lang, t }: { item: ModelUsageDetailRow; window: BudgetWindow; selectedKey?: string; lang: 'zh' | 'en'; t: (key: string) => string }) {
  if (item.account.provider === 'trae') return <span className="text-xs text-muted">{t('modelUsageUnknown')}</span>
  const estimate = selectedBudget(item, selectedKey)
  if (!estimate) return <span className="text-xs text-muted">{t('modelUsageBudgetUnknown')}</span>
  return <BudgetProgress item={estimate} window={window} lang={lang} t={t} />
}

function BudgetProgress({ item, window, lang, t }: { item: BudgetEstimate; window: BudgetWindow; lang: 'zh' | 'en'; t: (key: string, vars?: Record<string, string | number>) => string }) {
  const is24h = window === '24h'
  const used = is24h ? (item.observed_24h_coverage === 'complete' ? item.observed_24h_tokens : null) : (item.coverage_complete ? item.observed_since_reset : null)
  const reference = is24h ? item.estimated_24h_budget_tokens : item.estimated_tokens
  const remaining = is24h ? item.estimated_24h_remaining : item.estimated_remaining
  const percentage = budgetProgressPercent(used, reference)
  const exceeded = budgetProgressExceedsReference(used, reference)
  const title = is24h ? t('modelUsageBudgetWindow24h') : t('modelUsageBudgetWindowReset')
  return <div className="min-w-40 space-y-1.5">
    <div className="flex items-center justify-between gap-2 text-xs"><span className="font-medium">{title}</span><span className="mono">{percentage == null ? '—' : `${percentage}%`}</span></div>
    {percentage == null ? <p className="text-[10px] leading-4 text-muted">{t('modelUsageBudgetProgressUnknown')}</p> : <Meter minValue={0} maxValue={100} value={percentage} aria-label={title} valueLabel={`${formatCount(used)} / ${formatCount(reference)}`} size="sm" color={exceeded ? 'warning' : 'default'}><Meter.Track><Meter.Fill /></Meter.Track></Meter>}
    <div className="mono text-[10px] text-muted">{t('modelUsageBudgetProgressUsed')}: {formatCount(used)} · {t('modelUsageBudgetProgressReference')}: {formatCount(reference)}</div>
    <div className="text-[10px] text-muted">{t('modelUsageBudgetProgressRemaining')}: {formatCount(remaining)}{exceeded ? ` · ${t('modelUsageBudgetProgressExceeded')}` : ''}</div>
    {item.estimated_at ? <div className="text-[10px] text-muted">{t('modelUsageBudgetProgressUpdated')}: {formatDate(item.estimated_at, lang)}</div> : null}
  </div>
}

function BudgetEvidence({ row, selectedKey, lang, t, onVariant }: { row: ModelUsageDetailRow; selectedKey?: string; lang: 'zh' | 'en'; t: (key: string, vars?: Record<string, string | number>) => string; onVariant: (key: string) => void }) {
  const budgets = row.budgets || []
  const item = selectedBudget(row, selectedKey)
  const variantOptions = budgets.map((budget) => ({
    id: workbuddyBudgetKey(budget),
    label: `${budget.plan || t('modelUsagePlanUnknown')} · ${budgetPathLabel(budget.request_path, t)}`,
  }))
  const cacheCoverage = row.usage.cache_known_requests + row.usage.cache_missing_requests
  return <details className="min-w-0 rounded-lg border border-separator bg-surface-secondary/50 p-2 text-xs">
    <summary className="cursor-pointer rounded-sm font-medium focus-visible:outline-2 focus-visible:outline-offset-2">{t('modelUsageBudgetEvidence')}{item ? ` · ${budgets.length > 1 ? t('modelUsageBudgetVariants', { count: budgets.length }) : t('modelUsageBudgetSamples', { count: item.sample_count })}` : ''}</summary>
    <div className="mt-2 space-y-2">
      <section className="rounded-md border border-separator p-2">
        <div className="mb-1 font-medium">{t('modelUsageColCache')}</div>
        <dl className="grid grid-cols-2 gap-x-3 gap-y-1 text-[10px] sm:grid-cols-3">
          <EvidenceFact label={t('modelUsageColCache')} value={`${formatCount(row.usage.cache_read_tokens)} / ${formatCount(row.usage.cache_write_tokens)}`} />
          <EvidenceFact label={t('modelUsageCacheCoverage')} value={`${formatCount(row.usage.cache_known_requests)} / ${formatCount(cacheCoverage)} · ${t('modelUsageCacheHint')}`} />
        </dl>
      </section>
      {row.account.provider === 'trae' ? <p className="text-[10px] text-muted">{t('modelUsageTraeBudgetUnknown')}</p> : item ? <>
        {budgets.length > 1 ? <div><Label className="text-[10px] text-muted">{t('modelUsageBudgetVariant')}</Label><FilterSelect value={selectedKey || workbuddyBudgetKey(item)} onChange={onVariant} ariaLabel={t('modelUsageBudgetVariant')} options={variantOptions} width="full" /></div> : null}
        <dl className="grid grid-cols-2 gap-x-3 gap-y-1 text-[10px] sm:grid-cols-3">
          <EvidenceFact label={t('modelUsagePlan')} value={item.plan || t('modelUsagePlanUnknown')} />
          <EvidenceFact label={t('modelUsageRequestPath')} value={budgetPathLabel(item.request_path, t)} />
          <EvidenceFact label={t('modelUsageBudgetBasis')} value={item.basis || t('modelUsageBudgetBasisUnknown')} />
          <EvidenceFact label={t('modelUsageRecentSamples')} value={`${item.sample_count} / ${item.complete_windows}`} />
          <EvidenceFact label={t('modelUsageObservedSinceReset')} value={item.observed_since_reset == null ? t('modelUsageUnknown') : formatCount(item.observed_since_reset)} />
          <EvidenceFact label={t('modelUsageResetAt')} value={item.reset_at ? formatDate(item.reset_at, lang) : t('modelUsageUnknown')} />
          <EvidenceFact label={t('modelUsageCoverage')} value={item.coverage_state} />
          <EvidenceFact label={t('modelUsageObserved24h')} value={item.observed_24h_tokens == null ? t('modelUsageUnknown') : formatCount(item.observed_24h_tokens)} />
          <EvidenceFact label={t('modelUsageBudget24hCoverage')} value={item.observed_24h_coverage} />
        </dl>
        {item.reference ? <p className="text-[10px] text-muted">{t('modelUsageBudgetStrongReference')}: {formatCount(item.reference.estimated_tokens)} · {item.reference.account_count} {t('modelUsageBudgetAccountsLabel')} / {item.reference.sample_count} {t('modelUsageBudgetWindowsLabel')} · {item.reference.plan || t('modelUsagePlanUnknown')} · {budgetPathLabel(item.reference.request_path, t)}</p> : null}
        {item.weak_references?.length ? <div className="space-y-1 text-[10px] text-muted">{item.weak_references.map((reference, index) => <p key={`${reference.scope}/${reference.request_path}/${index}`}>{t('modelUsageBudgetWeakReference')}: {reference.estimated_tokens == null ? t('modelUsageUnknown') : formatCount(reference.estimated_tokens)} · {reference.account_count} {t('modelUsageBudgetAccountsLabel')} · {reference.scope} · {reference.request_path ? budgetPathLabel(reference.request_path, t) : t('modelUsageRequestPathUnknown')}</p>)}</div> : null}
        {item.low_confidence_references?.length ? <div className="space-y-1 text-[10px] text-muted">{item.low_confidence_references.map((reference, index) => <p key={`${reference.account_id}/${index}`}>{t('modelUsageBudgetReferenceSamples')}: {reference.plan || t('modelUsagePlanUnknown')} · {reference.request_path ? budgetPathLabel(reference.request_path, t) : t('modelUsageRequestPathUnknown')} · {reference.sample.tokens == null ? t('modelUsageUnknown') : formatCount(reference.sample.tokens)} · {reference.sample.confidence}</p>)}</div> : null}
        <BudgetGuardEditor item={item} t={t} />
        <p className="text-[10px] leading-4 text-muted">{t('modelUsageBudgetCaveat')}</p>
      </> : <p className="text-[10px] text-muted">{t('modelUsageBudgetUseUnknown')}</p>}
    </div>
  </details>
}

function EvidenceFact({ label, value }: { label: string; value: string }) {
  return <div className="min-w-0"><dt className="text-muted">{label}</dt><dd className="mt-0.5 break-words">{value}</dd></div>
}

function BudgetGuardEditor({ item, t }: { item: BudgetEstimate; t: (key: string, vars?: Record<string, string | number>) => string }) {
  const [open, setOpen] = useState(false)
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [policy, setPolicy] = useState<AccountUsagePolicy | null>(null)
  const [value, setValue] = useState('')
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const modelKey = canonicalUsageModelKey(item.model)
  const configured = Boolean(policy && hasModelUsageLimit(policy, item.model))

  async function openEditor() {
    setOpen(true)
    setLoading(true)
    setError('')
    setSaved(false)
    try {
      const current = (await fetchAccountUsage(item.account_id, 1)).policy
      setPolicy(current)
      setValue(hasModelUsageLimit(current, item.model) ? String(current.model_daily_tokens[modelKey]) : String(safeBudgetGuardSuggestion(item) ?? ''))
    } catch (reason) { setError(t('modelUsageGuardLoadError', { msg: reason instanceof Error ? reason.message : String(reason) })) }
    finally { setLoading(false) }
  }

  async function save() {
    if (!policy || loading || saving || configured) return
    const parsed = Number(value)
    if (!/^\d+$/.test(value.trim()) || !Number.isSafeInteger(parsed) || parsed <= 0) { setError(t('modelUsageGuardInvalid')); return }
    setSaving(true)
    setError('')
    setSaved(false)
    try {
      const current = (await fetchAccountUsage(item.account_id, 1)).policy
      if (hasModelUsageLimit(current, item.model)) {
        setPolicy(current)
        setValue(String(current.model_daily_tokens[modelKey]))
        setError(t('modelUsageGuardChanged'))
        return
      }
      const next: AccountUsagePolicy = { ...current, model_daily_tokens: { ...(current.model_daily_tokens || {}), [modelKey]: parsed } }
      await updateAccountUsagePolicy(item.account_id, next)
      setPolicy(next)
      setSaved(true)
    } catch (reason) { setError(t('modelUsageGuardSaveError', { msg: reason instanceof Error ? reason.message : String(reason) })) }
    finally { setSaving(false) }
  }

  return <div className="border-t border-separator pt-2">
    {!open ? <Button size="sm" variant="secondary" onPress={() => void openEditor()}>{t('modelUsageGuardApplyAction')}</Button> : <div className="space-y-2 rounded-lg border border-separator p-2">
      <div className="text-xs font-medium">{t('modelUsageGuardApplyTitle')}</div>
      <p className="text-[10px] leading-4 text-muted">{t('modelUsageGuardApplyHint')}</p>
      {loading ? <Skeleton className="h-8 w-full rounded-lg" /> : policy ? configured ? <p className="text-xs text-warning">{t('modelUsageGuardExisting')} · {value || '0'}</p> : <>
        <Label className="text-xs">{t('modelUsageGuardTokenLimit')}</Label>
        <Input type="number" min={1} step={1} inputMode="numeric" value={value} aria-label={t('modelUsageGuardTokenLimit')} onChange={(event) => setValue(event.target.value)} />
        <p className="text-[10px] text-muted">{safeBudgetGuardSuggestion(item) != null ? t('modelUsageGuardSafePrefill') : t('modelUsageGuardManualOnly')}</p>
      </> : null}
      {error ? <p role="alert" className="text-xs text-danger">{error}</p> : null}
      {saved ? <p role="status" className="text-xs text-success">{t('modelUsageGuardSaved')}</p> : null}
      <div className="flex flex-wrap gap-2"><Button size="sm" isPending={saving} isDisabled={loading || saving || configured || !policy} onPress={() => void save()}>{t('modelUsageGuardSave')}</Button><Button size="sm" variant="ghost" isDisabled={saving} onPress={() => setOpen(false)}>{t('cancel')}</Button></div>
    </div>}
  </div>
}

function DiagnosticsPanel({ t, lang, rows, modelValue, accountValue, modelOptions, routeOptions, diagnostics, loading, error, page, pages, onModel, onAccount, onPage, onRetry, provider, region, probeBusy, probeResult, probeError, onProbe, onCancelProbe }: {
  t: (key: string, vars?: Record<string, string | number>) => string
  lang: 'zh' | 'en'
  rows: ModelUsageOverviewRow[]
  modelValue: string
  accountValue: string
  modelOptions: { id: string; label: string }[]
  routeOptions: { id: string; label: string }[]
  diagnostics: ModelUsageDiagnosticsResponse | null
  loading: boolean
  error: string
  page: number
  pages: number
  onModel: (value: string) => void
  onAccount: (value: string) => void
  onPage: (value: number) => void
  onRetry: () => void
  provider: string
  region: string
  probeBusy: boolean
  probeResult: string
  probeError: string
  onProbe: () => void
  onCancelProbe: () => void
}) {
  const selected = rows.find((row) => JSON.stringify([row.provider, row.region, row.model]) === modelValue)
  const options = modelOptions.length ? modelOptions : [{ id: '', label: t('modelUsageSelectModel') }]
  const routeEntries = diagnostics?.routes || []
  const errorEntries = diagnostics?.data.errors || []
  const routePages = Math.max(1, Math.ceil((diagnostics?.routes_total || 0) / 20))
  return <div className="space-y-4">
    <Card><Card.Content className="space-y-3 p-4">
      <div><div className="text-sm font-semibold">{t('modelUsageDiagnosticsTab')}</div><p className="mt-1 text-xs leading-5 text-muted">{t('modelUsageDiagnosticsHint')}</p></div>
      <div className="grid min-w-0 gap-3 sm:grid-cols-2">
        <div className="min-w-0"><Label className="mb-1 block text-xs text-muted">{t('modelUsageDiagnosticModel')}</Label><FilterSelect value={modelValue} onChange={onModel} ariaLabel={t('modelUsageDiagnosticModel')} options={options} width="full" /></div>
        <div className="min-w-0"><Label className="mb-1 block text-xs text-muted">{t('modelUsageDiagnosticAccount')}</Label><FilterSelect value={accountValue} onChange={onAccount} ariaLabel={t('modelUsageDiagnosticAccount')} options={[{ id: '', label: t('modelUsageAllAccounts') }, ...routeOptions]} width="full" /></div>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Button isDisabled={probeBusy || !selected || !accountValue} onPress={onProbe}><PaperPlaneTilt size={15} />{probeBusy ? t('modelUsageProbeRunning') : t('modelUsageProbeRun')}</Button>
        {probeBusy ? <Button variant="secondary" onPress={onCancelProbe}>{t('cancel')}</Button> : null}
        <span className="text-xs text-muted">{t('modelUsageProbeIdleHint')}</span>
      </div>
      {probeResult ? <div role="status" className="rounded-lg border border-border bg-surface-secondary p-3 text-xs"><div className="font-medium">{t('modelUsageProbeResult')}</div><div className="mono mt-1 break-all text-muted">{probeResult}</div></div> : null}
      {probeError ? <PageAlert status="warning" title={probeError} /> : null}
      {!provider || !region ? null : <p className="text-[10px] text-muted">{provider}/{region}</p>}
    </Card.Content></Card>
    {error ? <PageAlert title={t('modelUsageLoadFailed', { msg: error })} description={t('modelUsageDiagnosticsRetry')} /> : null}
    {loading && !diagnostics ? <DetailSkeleton /> : <section className="grid min-w-0 gap-4 xl:grid-cols-2" aria-busy={loading}>
      <Card className="min-w-0"><Card.Header className="px-4 pt-4"><Card.Title>{t('modelUsageRouteCandidates')}</Card.Title></Card.Header><Card.Content className="space-y-2 p-3">
        {!modelValue ? <p className="text-sm text-muted">{t('modelUsageNoRouteCandidates')}</p> : routeEntries.length ? routeEntries.map((candidate) => <RouteItem key={candidate.id} candidate={candidate} lang={lang} t={t} />) : <p className="text-sm text-muted">{t('modelUsageNoRoute')}</p>}
        {diagnostics && diagnostics.routes_total > 20 ? <Pager page={diagnostics.routes_page} pages={routePages} hasMore={diagnostics.routes_has_more} loading={loading} onPage={onPage} previous={t('modelUsagePrevious')} next={t('modelUsageNext')} /> : null}
      </Card.Content></Card>
      <Card className="min-w-0"><Card.Header className="px-4 pt-4"><Card.Title>{t('modelUsageRecentErrorsTitle')}</Card.Title></Card.Header><Card.Content className="space-y-2 p-3">
        {errorEntries.length ? errorEntries.map((item) => <DiagnosticError key={item.attempt_id} item={item} lang={lang} t={t} />) : <p className="text-sm text-muted">{t('modelUsageNoDiagnosticErrors')}</p>}
        {diagnostics && diagnostics.data.total > 20 ? <Pager page={page} pages={pages} hasMore={diagnostics.data.has_more} loading={loading} onPage={onPage} previous={t('modelUsagePrevious')} next={t('modelUsageNext')} /> : null}
      </Card.Content></Card>
    </section>}
    <div className="flex justify-end"><Button size="sm" variant="secondary" isDisabled={loading} onPress={onRetry}><ArrowClockwise size={14} />{t('refresh')}</Button></div>
  </div>
}

function RouteItem({ candidate, lang, t }: { candidate: RouteCandidate; lang: 'zh' | 'en'; t: (key: string) => string }) {
  const recovery = candidate.model_cooldown_until || candidate.account_cooldown_until
  return <div className="rounded-xl border border-border p-3">
    <div className="flex flex-wrap items-start justify-between gap-2"><div className="min-w-0"><div className="mono break-all text-xs">{candidate.id}</div><div className="mt-1 text-[10px] text-muted">{candidate.provider}/{candidate.region} · {t('modelUsageInFlight')}: {formatCount(candidate.in_flight)} / {formatCount(candidate.max_in_flight)}</div></div><Chip size="sm" variant="soft" color={candidate.schedulable ? 'success' : 'warning'}>{reasonLabel(candidate.reason, t)}</Chip></div>
    {recovery ? <div className="mt-2 text-[10px] text-muted">{t('modelUsageCoolingUntil')}: {formatDate(recovery, lang)}</div> : candidate.reason !== 'available' ? <div className="mt-2 text-[10px] text-muted">{t('modelUsageUnknownRecovery')}</div> : null}
    {candidate.model_cooldown_evidence || candidate.account_cooldown_evidence || candidate.account_quota_evidence ? <details className="mt-2 text-[10px]"><summary className="cursor-pointer rounded-sm text-muted focus-visible:outline-2 focus-visible:outline-offset-2">{t('modelUsageTechnicalDetails')}</summary><div className="mono mt-1 space-y-1 break-words text-muted">
      {candidate.model_cooldown_evidence ? <p>model · {candidate.model_cooldown_evidence.code || '—'} · {candidate.model_cooldown_evidence.scope || '—'} · {candidate.model_cooldown_evidence.source || '—'} · reset {formatDate(candidate.model_cooldown_evidence.upstream_reset_at, lang)} · observed {formatDate(candidate.model_cooldown_evidence.last_observed_at, lang)}</p> : null}
      {candidate.account_cooldown_evidence ? <p>account · {candidate.account_cooldown_evidence.code || '—'} · {candidate.account_cooldown_evidence.scope || '—'} · {candidate.account_cooldown_evidence.source || '—'} · reset {formatDate(candidate.account_cooldown_evidence.upstream_reset_at, lang)} · observed {formatDate(candidate.account_cooldown_evidence.last_observed_at, lang)}</p> : null}
      {candidate.account_quota_evidence ? <p>quota · {candidate.account_quota_evidence.code || '—'} · {candidate.account_quota_evidence.scope || '—'} · {candidate.account_quota_evidence.source || '—'} · reset {formatDate(candidate.account_quota_evidence.upstream_reset_at, lang)}</p> : null}
    </div></details> : null}
  </div>
}

function DiagnosticError({ item, lang, t }: { item: ModelUsageError; lang: 'zh' | 'en'; t: (key: string) => string }) {
  return <div className="rounded-xl border border-border p-3">
    <div className="flex flex-wrap items-start justify-between gap-2"><div className="min-w-0"><div className="text-sm font-medium">{errorSummary(item, t)}</div><p className="mt-1 text-xs leading-5 text-muted">{errorSuggestion(item, t)}</p><div className="mono mt-1 break-all text-[10px] text-muted">{item.provider}/{item.region}/{item.model} · {item.code || item.kind || t('modelUsageUnknownScope')} · {item.status}</div></div><Link className="rounded-lg px-2 py-1 text-xs text-accent hover:bg-surface-secondary focus-visible:outline-2 focus-visible:outline-offset-2" to={`/logs?id=${encodeURIComponent(item.request_id)}`}>{t('modelUsageOpenLog')}</Link></div>
    <details className="mt-2 text-[10px] text-muted"><summary className="cursor-pointer rounded-sm focus-visible:outline-2 focus-visible:outline-offset-2">{t('modelUsageTechnicalDetails')}</summary><div className="mono mt-1 break-all">request {item.request_id} · attempt {item.attempt_id} · account {item.account_id} · scope {item.scope || t('modelUsageUnknownScope')}{item.finish_reason ? ` · finish ${item.finish_reason} (${item.finish_reason_source || 'unknown'})` : ''}{item.upstream_reset_at ? ` · upstream reset ${formatDate(item.upstream_reset_at, lang)}` : ''}</div></details>
  </div>
}
