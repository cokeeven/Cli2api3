import { useEffect, useMemo, useRef, useState } from 'react'
import { Button, Card, Chip, Input, Label, ListBox, Meter, Select } from '@heroui/react'
import { ArrowClockwise, ChartLineUp, PaperPlaneTilt } from '@phosphor-icons/react'
import { Link } from 'react-router-dom'
import { fetchModelUsage, probeModel, type BudgetEstimate, type ModelUsageResponse, type ModelUsageRow, type RouteCandidate } from '@/api/model-usage'
import { fetchModels } from '@/api/overview'
import type { ModelInfo } from '@/api/types'
import { useI18n } from '@/hooks/useI18n'
import { FilterSelect } from '@/components/ui/FilterSelect'
import { EmptyPanel } from '@/components/ui/EmptyPanel'
import { PageAlert } from '@/components/ui/PageAlert'
import { budgetProgressExceedsReference, budgetProgressPercent, workbuddyBudgetKey } from '@/lib/model-usage'
import { fetchAccountUsage, updateAccountUsagePolicy, type AccountUsagePolicy } from '@/api/usage'
import { canonicalUsageModelKey, hasModelUsageLimit, safeBudgetGuardSuggestion } from '@/lib/usagePolicy'

type WindowPreset = '24h' | '7d' | '30d'
const EMPTY_BUDGETS: BudgetEstimate[] = []

function queryWindow(preset: WindowPreset) {
  const to = new Date()
  const from = new Date(to)
  from.setTime(from.getTime() - (preset === '24h' ? 24 : preset === '7d' ? 168 : 720) * 60 * 60 * 1000)
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

function nextBeijingMidnight() {
  const parts = new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit' })
    .formatToParts(new Date()).reduce<Record<string, string>>((result, part) => { if (part.type !== 'literal') result[part.type] = part.value; return result }, {})
  return new Date(Date.UTC(Number(parts.year), Number(parts.month) - 1, Number(parts.day) + 1) - 8 * 60 * 60 * 1000)
}

function formatBeijingDate(value: Date, lang: 'zh' | 'en') {
  return new Intl.DateTimeFormat(lang === 'zh' ? 'zh-CN' : 'en-US', {
    timeZone: 'Asia/Shanghai', year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  }).format(value)
}

function valueLabel(value: number | null | undefined) {
  return value == null ? '—' : formatCount(value)
}

function errorSummary(item: ModelUsageResponse['data']['recent'][number], t: (key: string) => string) {
  const code = item.code || ''
  if (code === 'application_shutdown') return t('modelUsageErrAppShutdown')
  if (code === '6004') return t('modelUsageErr6004')
  if (code === '3004') return t('modelUsageErr3004')
  if (code === '4007') return t('modelUsageErr4007')
  if (code === '4011') return t('modelUsageErr4011')
  if (code === '4021') return t('modelUsageErr4021')
  if (code === '4022') return t('modelUsageErrContext')
  if (code === 'length') return t('modelUsageErrLength')
  if (item.kind === 'concurrency_capacity') return t('modelUsageErrConcurrencyCapacity')
  if (item.kind === 'auth') return t('modelUsageErrUpstreamAuth')
  if (item.kind === 'quota') return t('modelUsageErrCredits')
  if (item.kind === 'rate_limit') return t('modelUsageErrRateLimit')
  return t('modelUsageErrUnknown')
}

function errorSuggestion(item: ModelUsageResponse['data']['recent'][number], t: (key: string) => string) {
  const code = item.code || ''
  if (code === 'application_shutdown') return t('modelUsageSuggestAppShutdown')
  if (code === '6004') return t('modelUsageSuggest6004')
  if (code === '3004' || code === '4007' || code === '4011') return t('modelUsageSuggestLimit')
  if (code === '4021') return t('modelUsageSuggestUnknown')
  if (item.kind === 'concurrency_capacity') return t('modelUsageSuggestLocalCapacity')
  if (code === '4022') return t('modelUsageSuggestContext')
  if (code === 'length') return t('modelUsageSuggestLength')
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

function CooldownEvidenceDetails({ evidence, lang, t }: { evidence: NonNullable<RouteCandidate['model_cooldown_evidence']>; lang: 'zh' | 'en'; t: (key: string) => string }) {
  const dateValue = (value?: string) => value ? formatDate(value, lang) : t('modelUsageUnknown')
  const rows = [
    [t('modelUsageEvidenceCode'), evidence.code || t('modelUsageUnknown')],
    [t('modelUsageEvidenceSource'), evidence.source || t('modelUsageUnknown')],
    [t('modelUsageEvidenceScope'), evidence.scope || t('modelUsageUnknownScope')],
    [t('modelUsageEvidenceReportedScope'), evidence.reported_scope || t('modelUsageUnknownScope')],
    [t('modelUsageEvidenceFirstObserved'), dateValue(evidence.first_observed_at)],
    [t('modelUsageEvidenceLastObserved'), dateValue(evidence.last_observed_at)],
    [t('modelUsageUpstreamReset'), dateValue(evidence.upstream_reset_at)],
    [t('modelUsageLocalBackoff'), dateValue(evidence.local_backoff_until)],
  ]
  return <div className="mt-1 space-y-0.5 text-[10px] text-muted" aria-label={t('modelUsageCooldownEvidenceTitle')}>
    {rows.map(([label, value]) => <div key={label}>{label}: {value}</div>)}
  </div>
}

function QuotaEvidenceDetails({ evidence, lang, t }: { evidence: NonNullable<RouteCandidate['account_quota_evidence']>; lang: 'zh' | 'en'; t: (key: string) => string }) {
  const dateValue = (value?: string) => value ? formatDate(value, lang) : t('modelUsageUnknown')
  const rows = [
    [t('modelUsageEvidenceCode'), evidence.code || t('modelUsageUnknown')],
    [t('modelUsageEvidenceSource'), evidence.source || t('modelUsageUnknown')],
    [t('modelUsageEvidenceScope'), evidence.scope || t('modelUsageUnknownScope')],
    [t('modelUsageEvidenceReportedScope'), evidence.reported_scope || t('modelUsageUnknownScope')],
    [t('modelUsageEvidenceFirstObserved'), dateValue(evidence.first_observed_at)],
    [t('modelUsageEvidenceLastObserved'), dateValue(evidence.last_observed_at)],
    [t('modelUsageUpstreamReset'), dateValue(evidence.upstream_reset_at)],
  ]
  return <div className="mt-1 space-y-0.5 text-[10px] text-muted" aria-label={t('modelUsageQuotaEvidenceTitle')}>
    {rows.map(([label, value]) => <div key={label}>{label}: {value}</div>)}
  </div>
}

function usageCoverage(row: ModelUsageRow) {
  const total = row.known_usage_attempts + row.estimated_usage_attempts + row.missing_usage_attempts
  return `${row.known_usage_attempts}/${total}`
}

function budgetFor(data: ModelUsageResponse | null, row: ModelUsageRow): BudgetEstimate | undefined {
	if (row.provider !== 'workbuddy') return undefined
	const matches = (data?.workbuddy_budgets ?? []).filter((item) => item.account_id === row.account_id && item.region === row.region && item.model === row.model)
	return matches.length === 1 ? matches[0] : undefined
}

function groupBudgets(items: BudgetEstimate[]) {
  const groups = new Map<string, BudgetEstimate[]>()
  for (const item of items) {
    const key = JSON.stringify([item.region, item.model])
    groups.set(key, [...(groups.get(key) || []), item])
  }
  return [...groups.entries()].map(([key, rows]) => ({ key, region: rows[0]?.region || '', model: rows[0]?.model || '', rows }))
}

function budgetPathLabel(path: string | undefined, t: (key: string) => string) {
  switch (path) {
    case 'openai_chat_completions': return t('modelUsagePathChatCompletions')
    case 'openai_responses': return t('modelUsagePathResponses')
    case 'anthropic_messages': return t('modelUsagePathAnthropicMessages')
    default: return t('modelUsageRequestPathUnknown')
  }
}

function budget24hCoverageKey(state: string) {
  switch (state) {
    case 'complete': return 'modelUsageBudget24hCoverageComplete'
    case 'missing_usage_lower_bound': return 'modelUsageBudget24hCoverageLowerBound'
    case 'range_boundary_lower_bound': return 'modelUsageBudget24hCoverageRange'
    case 'truncated_lower_bound': return 'modelUsageBudget24hCoverageTruncated'
    case 'no_observed_attempts': return 'modelUsageBudget24hCoverageNoAttempts'
    default: return 'modelUsageBudget24hCoverageUnknown'
  }
}

function budgetSampleScopeKey(scope: string) {
  switch (scope) {
    case 'known_plan_and_request_path': return 'modelUsageBudgetSampleScopeMatched'
    case 'plan_unverified_path_separated': return 'modelUsageBudgetSampleScopeUnknownPlan'
    case 'request_path_unverified': return 'modelUsageBudgetSampleScopeUnknownPath'
    default: return 'modelUsageBudgetSampleScopeUnknownBoth'
  }
}

function candidateRows(data: ModelUsageResponse | null): { route: string; candidate: RouteCandidate }[] {
  if (!data) return []
  return data.routes.flatMap((route) => route.candidates.map((candidate) => ({ route: `${route.provider}/${route.region}/${route.model}`, candidate })))
}

export function ModelUsagePage() {
  const { t, lang } = useI18n()
  const [data, setData] = useState<ModelUsageResponse | null>(null)
  const [models, setModels] = useState<ModelInfo[]>([])
  const [provider, setProvider] = useState('')
  const [region, setRegion] = useState('')
  const [model, setModel] = useState('')
  const [account, setAccount] = useState('')
  const [preset, setPreset] = useState<WindowPreset>('24h')
  const [budgetOffset, setBudgetOffset] = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [probeBusy, setProbeBusy] = useState(false)
  const [probeResult, setProbeResult] = useState('')
  const [probeError, setProbeError] = useState('')
  const [refreshEpoch, setRefreshEpoch] = useState(0)
  const probeController = useRef<AbortController | null>(null)

  useEffect(() => {
    let cancelled = false
    void fetchModels().then((result) => { if (!cancelled) setModels(result.data || []) }).catch(() => undefined)
    return () => { cancelled = true }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    const timer = window.setTimeout(() => {
      setLoading(true)
      setError('')
      const range = queryWindow(preset)
      void fetchModelUsage({ provider, region, model, account, ...range, limit: 500, budget_limit: 200, budget_offset: budgetOffset }, controller.signal)
        .then((result) => setData(result))
        .catch((reason: unknown) => {
          if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : String(reason))
        })
        .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    }, 120)
    return () => { window.clearTimeout(timer); controller.abort() }
  }, [provider, region, model, account, preset, budgetOffset, refreshEpoch])

  const modelOptions = useMemo(() => {
    const seen = new Map<string, string>()
    for (const item of models) {
      if (item.provider && item.provider !== 'workbuddy' && item.provider !== 'trae') continue
      if (provider && item.provider && item.provider !== provider) continue
      if (region && item.region && item.region !== region) continue
      seen.set(item.id, item.display_name || item.id)
    }
    for (const row of data?.data.rows || []) seen.set(row.model, row.model)
    return [...seen].sort(([a], [b]) => a.localeCompare(b)).map(([id, label]) => ({ id, label: `${label} · ${id}` }))
  }, [models, data, provider, region])
  const accountOptions = useMemo(() => (data?.accounts || [])
    .map((item) => ({ id: item.id, label: `${item.name || item.id} · ${item.provider}/${item.region}` })), [data])

  const totals = useMemo(() => {
    const rows = data?.data.rows || []
    return rows.reduce((result, row) => {
      result.attempts += row.attempts
      result.known += row.known_usage_attempts
      result.missing += row.missing_usage_attempts
      result.prompt += row.prompt_tokens || 0
      result.completion += row.completion_tokens || 0
      return result
    }, { attempts: 0, known: 0, missing: 0, prompt: 0, completion: 0 })
  }, [data])
  const candidateList = useMemo(() => candidateRows(data), [data])
  const schedulable = candidateList.filter(({ candidate }) => candidate.schedulable).length
  const routeTotal = candidateList.length
  const accountNames = useMemo(() => new Map((data?.accounts || []).map((item) => [item.id, item.name || item.id])), [data])
  const workbuddyBudgets = data?.workbuddy_budgets ?? EMPTY_BUDGETS
  const budgetGroups = useMemo(() => groupBudgets(workbuddyBudgets), [workbuddyBudgets])

  async function runProbe() {
    if (!account || !model || probeBusy) return
    setProbeBusy(true)
    setProbeResult('')
    setProbeError('')
    const controller = new AbortController()
    probeController.current = controller
    try {
      const result = await probeModel(account, model, controller.signal)
      setProbeResult(`${result.request_id || '—'} · ${result.observed_account_id || '—'} · ${result.finish_reason || '—'} (${result.finish_reason_source || 'unknown'}) · request max_tokens ${result.requested_max_tokens} / actual completion_tokens ${result.completion_tokens ?? '—'} · usage ${result.prompt_tokens ?? '—'}/${result.completion_tokens ?? '—'} (${result.usage_source || 'unknown'}) · ${result.output || ''} · build ${result.build_fingerprint || '—'}`)
      if (!result.ok) setProbeError(result.error_message || result.error_code || t('modelUsageProbeFailed'))
      else setProbeError('')
      await fetchModelUsage({ provider, region, model, account, ...queryWindow(preset), limit: 500, budget_limit: 200, budget_offset: budgetOffset }).then(setData)
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
  ]
  const presetOptions = [
    { id: '24h', label: t('modelUsage24h') },
    { id: '7d', label: t('modelUsage7d') },
    { id: '30d', label: t('modelUsage30d') },
  ]

  return (
    <div className="mx-auto w-full max-w-[1500px] space-y-5 px-4 pb-10 pt-6 md:px-8">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <div className="flex items-center gap-2 text-muted"><ChartLineUp size={17} /><span className="text-xs font-semibold uppercase tracking-[0.16em]">{t('modelUsageEyebrow')}</span></div>
          <h1 className="mt-2 text-2xl font-semibold tracking-[-0.035em]">{t('modelUsageTitle')}</h1>
          <p className="mt-1 max-w-3xl text-sm leading-6 text-muted">{t('modelUsageIntro')}</p>
        </div>
        <Button variant="secondary" isDisabled={loading} onPress={() => setRefreshEpoch((value) => value + 1)}><ArrowClockwise size={15} />{t('refresh')}</Button>
      </div>

      {error ? <PageAlert title={t('modelUsageLoadFailed', { msg: error })} /> : null}

      <Card>
        <Card.Content className="grid grid-cols-1 gap-3 p-4 sm:grid-cols-2 xl:grid-cols-5">
          <FilterSelect value={provider} onChange={(value) => { setBudgetOffset(0); setProvider(value) }} ariaLabel={t('modelUsageProvider')} options={providerOptions} width="full" />
          <FilterSelect value={region} onChange={(value) => { setBudgetOffset(0); setRegion(value) }} ariaLabel={t('modelUsageRegion')} options={regionOptions} width="full" />
          <FilterSelect value={model} onChange={(value) => { setBudgetOffset(0); setModel(value) }} ariaLabel={t('modelUsageModel')} options={[{ id: '', label: t('modelUsageAllModels') }, ...modelOptions]} width="full" />
          <FilterSelect value={account} onChange={(value) => { setBudgetOffset(0); setAccount(value) }} ariaLabel={t('modelUsageAccount')} options={[{ id: '', label: t('modelUsageAllAccounts') }, ...accountOptions]} width="full" />
          <FilterSelect value={preset} onChange={(value) => { if (value === '24h' || value === '7d' || value === '30d') { setBudgetOffset(0); setPreset(value) } }} ariaLabel={t('modelUsageWindow')} options={presetOptions} width="full" />
          <span className="text-xs text-muted sm:col-span-2 sm:text-right xl:col-span-5">{t('modelUsageLocalOnly')} · {data ? `${formatDate(data.data.from, lang)} – ${formatDate(data.data.to, lang)}` : '—'}</span>
        </Card.Content>
      </Card>

      <section className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <SummaryCard title={t('modelUsageAttempts')} value={loading ? '…' : formatCount(totals.attempts)} hint={t('modelUsageAttemptsHint')} />
        <SummaryCard title={t('modelUsageKnownTokens')} value={loading ? '…' : formatCount(totals.prompt + totals.completion)} hint={`${t('modelUsagePrompt')}: ${formatCount(totals.prompt)} · ${t('modelUsageCompletion')}: ${formatCount(totals.completion)}`} />
        <SummaryCard title={t('modelUsageUsageCoverage')} value={loading ? '…' : `${formatCount(totals.known)} / ${formatCount(totals.known + totals.missing)}`} hint={t('modelUsageUnknownNotZero')} />
        <SummaryCard title={t('modelUsageSchedulable')} value={loading ? '…' : `${schedulable} / ${routeTotal}`} hint={t('modelUsageRouteSnapshot')} />
      </section>

      <section className="grid gap-4 xl:grid-cols-[minmax(0,1.65fr)_minmax(340px,0.85fr)]">
        <Card className="min-w-0">
          <Card.Header className="flex-row items-center justify-between gap-3 px-4 pt-4">
            <div><Card.Title>{t('modelUsageObservedModels')}</Card.Title><Card.Description>{t('modelUsageObservedModelsHint')}</Card.Description></div>
            {data?.data.truncated ? <Chip size="sm" variant="soft" color="warning">{t('modelUsageTruncated')}</Chip> : null}
          </Card.Header>
          <Card.Content className="p-0">
            {(data?.data.rows.length || 0) === 0 ? <div className="p-5"><EmptyPanel icon={<ChartLineUp size={22} />} title={t('modelUsageNoRows')} /></div> : (
              <div className="max-w-full overflow-x-auto">
                <table className="w-full min-w-[940px] border-collapse text-left text-xs">
                  <thead className="border-y border-separator bg-surface-secondary text-muted"><tr>
                    {[t('modelUsageColModel'), t('modelUsageColAccount'), t('modelUsageColCounts'), t('modelUsageColTokens'), t('modelUsageColCache'), t('modelUsageColCoverage'), t('modelUsageColLastSuccess')].map((label) => <th key={label} className="px-3 py-2.5 font-medium">{label}</th>)}
                  </tr></thead>
                  <tbody>
                    {data?.data.rows.map((row) => <ModelRow key={`${row.provider}/${row.region}/${row.account_id}/${row.model}`} row={row} data={data} accountName={accountNames.get(row.account_id) || row.account_id} lang={lang} t={t} />)}
                  </tbody>
                </table>
              </div>
            )}
          </Card.Content>
        </Card>

        <Card>
          <Card.Header className="px-4 pt-4"><Card.Title>{t('modelUsageAccountsTitle')}</Card.Title><Card.Description>{t('modelUsageAccountsHint')}</Card.Description></Card.Header>
          <Card.Content className="space-y-3 p-4 pt-2">
            {(data?.accounts || []).length === 0 ? <p className="text-sm text-muted">{t('modelUsageNoAccounts')}</p> : data?.accounts.map((item) => {
              const quotaKnown = item.quota != null
              return <div key={item.id} className="rounded-xl border border-border bg-surface-secondary/60 p-3">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="min-w-0"><div className="truncate text-sm font-medium">{item.name || item.id}</div><div className="mono mt-0.5 text-[10px] text-muted">{item.provider}/{item.region} · {item.id}</div></div>
                  <Chip size="sm" variant="soft" color={!item.enabled ? 'default' : item.ready ? 'success' : 'warning'}>{!item.enabled ? t('modelUsageDisabled') : item.ready ? t('modelUsageReady') : t('modelUsageNeedsLogin')}</Chip>
                </div>
                <div className="mt-3 grid grid-cols-2 gap-3 border-t border-separator pt-3 text-xs">
                  <div><div className="text-muted">{t('modelUsageAccountCredits')}</div><div className="mono mt-1">{quotaKnown ? `${formatCount(item.quota?.remaining)} ${item.quota?.unit || ''}` : t('modelUsageUnknown')}</div><div className="mt-1 text-[10px] text-muted">{quotaKnown ? `${t('modelUsageSnapshot')}: ${formatDate(item.quota?.fetched_at, lang)}` : t('modelUsageNoVerifiedBalance')}</div></div>
                  <div><div className="text-muted">{t('modelUsageModelBudget')}</div><div className="mono mt-1">{item.provider === 'trae' ? t('modelUsageUnknown') : t('modelUsageSeeEstimate')}</div><div className="mt-1 text-[10px] text-muted">{t('modelUsageBudgetNeverSchedules')}</div></div>
                </div>
              </div>
            })}
          </Card.Content>
        </Card>
      </section>

      <section className="grid gap-4 xl:grid-cols-2">
        <Card>
          <Card.Header className="px-4 pt-4"><Card.Title>{t('modelUsageRouteTitle')}</Card.Title><Card.Description>{t('modelUsageRouteHint')}</Card.Description></Card.Header>
          <Card.Content className="p-0">
            {candidateList.length === 0 ? <p className="px-4 pb-4 text-sm text-muted">{t('modelUsageNoRoute')}</p> : <div className="max-h-[420px] divide-y divide-separator overflow-auto">
              {candidateList.map(({ route, candidate }) => <div key={`${route}/${candidate.id}`} className="flex flex-wrap items-center justify-between gap-2 px-4 py-3">
                <div className="min-w-0"><div className="mono truncate text-xs">{route}</div><div className="mt-1 text-xs text-muted">{accountNames.get(candidate.id) || candidate.id} · {candidate.id}</div></div>
                <div className="text-right"><Chip size="sm" variant="soft" color={candidate.schedulable ? 'success' : 'warning'}>{reasonLabel(candidate.reason, t)}</Chip>{candidate.account_quota_evidence ? <QuotaEvidenceDetails evidence={candidate.account_quota_evidence} lang={lang} t={t} /> : null}{candidate.model_cooldown_until ? <div className="mt-1 text-[10px] text-muted">{t('modelUsageModelCooldown')}: {formatDate(candidate.model_cooldown_until, lang)}</div> : null}{candidate.model_cooldown_evidence ? <CooldownEvidenceDetails evidence={candidate.model_cooldown_evidence} lang={lang} t={t} /> : null}{candidate.account_cooldown_until ? <div className="mt-1 text-[10px] text-muted">{t('modelUsageAccountCooldown')}: {formatDate(candidate.account_cooldown_until, lang)}</div> : null}{candidate.account_cooldown_evidence ? <CooldownEvidenceDetails evidence={candidate.account_cooldown_evidence} lang={lang} t={t} /> : null}</div>
              </div>)}
            </div>}
          </Card.Content>
        </Card>

        <Card>
          <Card.Header className="px-4 pt-4"><Card.Title>{t('modelUsageBudgetTitle')}</Card.Title><Card.Description>{t('modelUsageBudgetHint')}</Card.Description></Card.Header>
          <Card.Content className="space-y-3 p-4 pt-2">
            {!provider || provider === 'workbuddy' ? workbuddyBudgets.length ? <div className="space-y-2">{budgetGroups.map((group) => <details key={group.key} className="rounded-xl border border-border bg-surface-secondary/50 p-3" open={budgetGroups.length === 1}>
              <summary className="cursor-pointer rounded-sm text-sm font-medium focus-visible:outline-2 focus-visible:outline-offset-2">{group.region} · {group.model} <span className="ml-1 text-xs font-normal text-muted">{t('modelUsageBudgetGroupAccounts', { count: group.rows.length })}</span></summary>
              <div className="mt-3 space-y-2">{group.rows.map((item) => <BudgetRow key={workbuddyBudgetKey(item)} item={item} accountName={accountNames.get(item.account_id) || item.account_id} accountNames={accountNames} lang={lang} t={t} />)}</div>
            </details>)}</div> : <div className="rounded-xl border border-border bg-surface-secondary p-3 text-sm text-muted">{t('modelUsageBudgetWaitingSamples')}</div> : <div className="rounded-xl border border-border bg-surface-secondary p-3 text-sm text-muted">{t('modelUsageTraeBudgetUnknown')}</div>}
            {(!provider || provider === 'workbuddy') && data ? <div className="flex flex-wrap items-center justify-between gap-2 border-t border-separator pt-3">
              <p className="text-xs text-muted">{t('modelUsageBudgetPage', { from: data.workbuddy_budgets_total ? data.workbuddy_budgets_offset + 1 : 0, to: data.workbuddy_budgets_offset + workbuddyBudgets.length, total: data.workbuddy_budgets_total })}</p>
              <div className="flex gap-2"><Button size="sm" variant="secondary" isDisabled={loading || data.workbuddy_budgets_offset <= 0} onPress={() => setBudgetOffset(Math.max(0, data.workbuddy_budgets_offset - data.workbuddy_budgets_limit))}>{t('modelUsageBudgetPrevious')}</Button><Button size="sm" variant="secondary" isDisabled={loading || !data.workbuddy_budgets_has_more} onPress={() => setBudgetOffset(data.workbuddy_budgets_offset + data.workbuddy_budgets_limit)}>{t('modelUsageBudgetNext')}</Button></div>
            </div> : null}
            {data?.workbuddy_budget_catalog_truncated ? <PageAlert status="warning" title={t('modelUsageBudgetCatalogTruncated')} /> : null}
            {data?.data.budget_observation_from ? <p className="text-[10px] text-muted">{t('modelUsageObservationRange', { from: formatDate(data.data.budget_observation_from, lang), to: formatDate(data.data.to, lang) })}</p> : null}
            <p className="text-[11px] leading-5 text-muted">{t('modelUsageBudgetCaveat')}</p>
            {data?.data.budget_observations_truncated ? <PageAlert status="warning" title={t('modelUsageBudgetTruncated')} /> : null}
          </Card.Content>
        </Card>
      </section>

      <Card>
        <Card.Header className="px-4 pt-4"><Card.Title>{t('modelUsageRecentErrors')}</Card.Title><Card.Description>{t('modelUsageErrorLayers')}</Card.Description></Card.Header>
        <Card.Content className="p-0">
          {(data?.data.recent.length || 0) === 0 ? <p className="px-4 pb-4 text-sm text-muted">{t('modelUsageNoErrors')}</p> : <div className="max-h-[360px] divide-y divide-separator overflow-auto">
            {data?.data.recent.map((item) => <div key={item.attempt_id} className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
              <div className="min-w-0"><div className="text-sm font-medium">{errorSummary(item, t)}</div><div className="mt-1 text-xs leading-5 text-muted">{errorSuggestion(item, t)}</div><details className="mt-1 text-[10px] text-muted"><summary className="cursor-pointer rounded-sm focus-visible:outline-2 focus-visible:outline-offset-2">{t('modelUsageTechnicalDetails')}</summary><div className="mono mt-1 break-all">{item.provider}/{item.region}/{item.model} · account {item.account_id} · {item.code || item.kind || t('modelUsageUnknownScope')} · scope {item.scope || t('modelUsageUnknownScope')} · {item.status}{item.finish_reason ? ` · finish ${item.finish_reason}` : ''}{item.finish_reason_source ? ` (${item.finish_reason_source})` : ''}{item.upstream_reset_at ? ` · upstream reset ${item.upstream_reset_at}` : ''} · request {item.request_id} · attempt {item.attempt_id}</div></details></div>
              <div className="flex items-center gap-2"><span className="text-[11px] text-muted">{item.upstream_reset_at ? `${t('modelUsageUpstreamReset')}: ${formatDate(item.upstream_reset_at, lang)}` : t('modelUsageNoUpstreamReset')}</span><Link className="rounded-lg px-2 py-1 text-xs text-accent hover:bg-surface-secondary focus-visible:outline-2 focus-visible:outline-offset-2" to={`/logs?id=${encodeURIComponent(item.request_id)}`}>{t('modelUsageOpenLog')}</Link></div>
            </div>)}
          </div>}
        </Card.Content>
      </Card>

      <Card>
        <Card.Header className="px-4 pt-4"><Card.Title>{t('modelUsageProbeTitle')}</Card.Title><Card.Description>{t('modelUsageProbeHint')}</Card.Description></Card.Header>
        <Card.Content className="flex flex-wrap items-end gap-3 p-4 pt-2">
          <div className="w-full sm:w-64"><ModelSelect label={t('modelUsageProbeModel')} value={model} onChange={setModel} options={modelOptions} placeholder={t('modelUsageSelectModel')} /></div>
          <div className="w-full sm:w-72"><ModelSelect label={t('modelUsageProbeAccount')} value={account} onChange={setAccount} options={accountOptions} placeholder={t('modelUsageSelectAccount')} /></div>
          <Button isDisabled={probeBusy || !model || !account} onPress={() => void runProbe()}><PaperPlaneTilt size={15} />{probeBusy ? t('modelUsageProbeRunning') : t('modelUsageProbeRun')}</Button>
          {probeBusy ? <Button variant="secondary" onPress={() => probeController.current?.abort()}>{t('cancel')}</Button> : null}
          {probeResult ? <div className="w-full rounded-lg border border-border bg-surface-secondary p-3 text-xs"><div className="font-medium">{t('modelUsageProbeResult')}</div><div className="mono mt-1 break-all text-muted">{probeResult}</div></div> : null}
          {probeError ? <div className="w-full"><PageAlert status="warning" title={probeError} /></div> : null}
        </Card.Content>
      </Card>
    </div>
  )
}

function SummaryCard({ title, value, hint }: { title: string; value: string; hint: string }) {
  return <Card><Card.Content className="p-4"><div className="text-xs text-muted">{title}</div><div className="mono mt-2 text-2xl font-semibold">{value}</div><div className="mt-1 text-[11px] leading-5 text-muted">{hint}</div></Card.Content></Card>
}

function ModelRow({ row, data, accountName, lang, t }: { row: ModelUsageRow; data: ModelUsageResponse; accountName: string; lang: 'zh' | 'en'; t: (key: string) => string }) {
  const budget = budgetFor(data, row)
  const budgetValue = budget?.estimated_remaining == null ? (!budget && data.workbuddy_budgets_total > (data.workbuddy_budgets?.length || 0) ? t('modelUsageBudgetSeePage') : t('modelUsageUnknown')) : `${formatCount(budget.estimated_remaining)} ${t('modelUsageEstimatedShort')}`
  return <tr className="border-b border-separator last:border-b-0">
    <td className="px-3 py-3"><div className="font-medium">{row.model}</div><div className="mt-1 text-[10px] text-muted">{row.provider}/{row.region}</div></td>
    <td className="px-3 py-3"><div className="max-w-40 truncate">{accountName}</div><div className="mono mt-1 text-[10px] text-muted">{row.account_id}</div></td>
    <td className="px-3 py-3"><div>{t('modelUsageRequests')}: {row.requests}</div><div className="mt-1 text-muted">{t('modelUsageAttempts')}: {row.attempts}</div><div className="mt-1 text-muted">✓ {row.succeeded_attempts} · × {row.failed_attempts} · {t('modelUsageCanceled')}: {row.canceled_attempts} · {t('modelUsageErrLength')}: {row.length_requests}</div></td>
    <td className="px-3 py-3"><div className="mono">{valueLabel(row.prompt_tokens)} / {valueLabel(row.completion_tokens)}</div><div className="mt-1 text-[10px] text-muted">{t('modelUsagePrompt')} / {t('modelUsageCompletion')}</div></td>
    <td className="px-3 py-3"><div className="mono">{valueLabel(row.cache_read_tokens)} / {valueLabel(row.cache_write_tokens)}</div><div className="mt-1 text-[10px] text-muted">{t('modelUsageCacheHint')} · {row.cache_known_requests}/{row.cache_known_requests + row.cache_missing_requests}</div></td>
    <td className="px-3 py-3"><div className="mono">{usageCoverage(row)}</div><div className="mt-1 text-[10px] text-muted">{t('modelUsageBudget')}: {row.provider === 'trae' ? t('modelUsageUnknown') : budgetValue}</div></td>
    <td className="px-3 py-3"><div>{formatDate(row.last_success_at, lang)}</div><Link className="mt-1 inline-block text-[10px] text-accent hover:underline" to={`/logs?model=${encodeURIComponent(row.model)}&account=${encodeURIComponent(row.account_id)}`}>{t('modelUsageOpenLogs')}</Link></td>
  </tr>
}

function BudgetRow({ item, accountName, accountNames, lang, t }: { item: BudgetEstimate; accountName: string; accountNames: Map<string, string>; lang: 'zh' | 'en'; t: (key: string, vars?: Record<string, string | number>) => string }) {
  const qualified = item.estimated_tokens != null
  const basisKey = item.basis === 'explicit_6004_reset_windows_weighted_recent' || item.basis === 'explicit_6004_reset_windows' ? 'modelUsageBudgetBasis6004' : item.basis === 'unknown_request_path' ? 'modelUsageBudgetPathUnknown' : item.basis === 'stale_samples' ? 'modelUsageBudgetStale' : item.basis === 'truncated_observations' ? 'modelUsageBudgetTruncated' : 'modelUsageBudgetBasisUnknown'
  const coverageKey = item.coverage_state === 'complete' ? 'modelUsageBudgetCoverageComplete' : item.coverage_state === 'usage_missing' ? 'modelUsageBudgetCoveragePartial' : item.coverage_state === 'observation_range_boundary' ? 'modelUsageBudgetCoverageBoundary' : item.coverage_state === 'no_billable_usage_observed' ? 'modelUsageBudgetNoBillableUsage' : item.coverage_state === 'upstream_restricted' ? 'modelUsageBudgetCoverageRestricted' : 'modelUsageBudgetCoverageUnknown'
  const coverage24hKey = budget24hCoverageKey(item.observed_24h_coverage)
  return <div className="rounded-xl border border-border bg-surface-secondary p-3">
    <div className="flex flex-wrap items-center justify-between gap-2"><div><div className="text-sm font-medium">{accountName} · {item.model}</div><div className="mono mt-1 text-[10px] text-muted">{item.region} · {item.plan || t('modelUsagePlanUnknown')} · {budgetPathLabel(item.request_path, t)}</div></div><Chip size="sm" variant="soft" color={item.restricted ? 'warning' : qualified ? 'default' : 'default'}>{item.restricted ? t('modelUsageRestricted') : qualified ? t('modelUsageEstimatedTag') : t('modelUsageUnknown')}</Chip></div>
    <div className="mt-3 grid grid-cols-2 gap-3 text-xs sm:grid-cols-4">
      <BudgetValue label={t('modelUsageRecentSamples')} value={`${item.sample_count} / ${item.complete_windows}`} />
      <BudgetValue label={t('modelUsageObservedRange')} value={item.sample_min_tokens == null ? '—' : `${formatCount(item.sample_min_tokens)}–${formatCount(item.sample_max_tokens)}`} />
      <BudgetValue label={t('modelUsageEstimatedBudget')} value={item.estimated_tokens == null ? t('modelUsageUnknown') : formatCount(item.estimated_tokens)} />
      <BudgetValue label={t('modelUsageEstimatedRemaining')} value={item.estimated_remaining == null ? t('modelUsageUnknown') : formatCount(item.estimated_remaining)} />
    </div>
    <div className="mt-2 text-[10px] text-muted">{item.sample_from ? t('modelUsageBudgetSampleRange', { from: formatDate(item.sample_from, lang), to: formatDate(item.sample_to, lang) }) : t('modelUsageBudgetNoRecentSample')}{item.sample_freshness === 'stale' ? ` · ${t('modelUsageBudgetStale')}` : ''} · {t('modelUsageBudgetWeights', { days: item.statistical_days, timezone: item.statistical_timezone })}</div>
    {item.restricted ? <div className="mt-1 text-xs text-warning">{t('modelUsageRestrictedAt', { at: formatDate(item.restriction_observed_at, lang) })} · {t('modelUsageCoolingUntil')}: {formatDate(item.reset_at, lang)}</div> : <div className="mt-1 text-[10px] text-muted">{t('modelUsageObservedSinceReset')}: {formatCount(item.observed_since_reset)}</div>}
    <div className="mt-1 text-[10px] text-muted">{t(basisKey)} · {item.coverage_state === 'usage_missing' ? t(coverageKey, { missing: item.missing_attempts }) : t(coverageKey)}</div>
    <div className="mt-2 rounded-lg border border-separator bg-surface p-2 text-xs"><div className="font-medium">{item.reference ? t('modelUsageBudgetStrongReference') : t('modelUsageReferenceTitle')}</div>{item.reference ? <div className="mt-1 text-muted">{t('modelUsageReferenceValue', { value: formatCount(item.reference.estimated_tokens), accounts: item.reference.account_count, samples: item.reference.sample_count, range: item.reference.sample_min_tokens == null ? '—' : `${formatCount(item.reference.sample_min_tokens)}–${formatCount(item.reference.sample_max_tokens)}` })}{item.reference.sample_from ? ` · ${formatDate(item.reference.sample_from, lang)}–${formatDate(item.reference.sample_to, lang)}` : ''} · {item.reference.confidence === 'single_complete_window' ? t('modelUsageBudgetSingleWindow') : t('modelUsageBudgetRepeatedWindows')}</div> : <div className="mt-1 text-muted">{item.plan && item.request_path ? t('modelUsageReferenceUnavailable') : t('modelUsageReferenceNeedsKnownPlanPath')}</div>}</div>
    <div className="mt-2 grid gap-2 rounded-lg border border-separator bg-surface p-2 text-xs sm:grid-cols-2">
      <div><div className="font-medium">{t('modelUsageBudgetObserved24h')}</div><div className="mono mt-1">{item.observed_24h_tokens == null ? t('modelUsageUnknown') : formatCount(item.observed_24h_tokens)}</div><div className="mt-1 text-[10px] text-muted">{t(coverage24hKey)}{item.observed_24h_missing_attempts ? ` · ${t('modelUsageBudgetMissing24h', { count: item.observed_24h_missing_attempts })}` : ''}</div></div>
      <div><div className="font-medium">{t('modelUsageBudgetEstimated24hRemaining')}</div><div className="mono mt-1">{item.estimated_24h_remaining == null ? t('modelUsageUnknown') : formatCount(item.estimated_24h_remaining)}</div><div className="mt-1 text-[10px] text-muted">{item.estimated_24h_basis === 'same_known_plan_and_request_path_reference' ? t('modelUsageBudgetStrongReference') : item.estimated_24h_basis === 'unverified_plan_same_region_model_reference' ? t('modelUsageBudgetWeakReference') : item.estimated_24h_basis === 'own_complete_reset_window_reference' ? t('modelUsageBudgetBasis6004') : t('modelUsageBudgetBasisUnknown')}</div></div>
    </div>
    <div className="mt-2 grid gap-2 sm:grid-cols-2">
      <BudgetProgress
        title={t('modelUsageBudgetResetProgress')}
        used={item.observed_since_reset}
        total={item.estimated_tokens}
        remaining={item.estimated_remaining}
        source={t('modelUsageBudgetBasis6004')}
        sampleRange={item.reset_at && item.estimated_at ? formatDate(item.reset_at, lang) + ' – ' + formatDate(item.estimated_at, lang) : t('modelUsageBudgetProgressUnavailable')}
        updatedAt={item.estimated_at ? formatDate(item.estimated_at, lang) : t('modelUsageBudgetProgressUnavailable')}
        usedLabel={t('modelUsageBudgetProgressUsed')}
        referenceLabel={t('modelUsageBudgetProgressReference')}
        remainingLabel={t('modelUsageBudgetProgressRemaining')}
        sampleRangeLabel={t('modelUsageBudgetProgressRange')}
        updatedLabel={t('modelUsageBudgetProgressUpdated')}
        exceedsReferenceLabel={t('modelUsageBudgetProgressExceeded')}
        unavailable={t('modelUsageBudgetProgressUnavailable')}
      />
      <BudgetProgress
        title={t('modelUsageBudget24hProgress')}
        used={item.observed_24h_coverage === 'complete' ? item.observed_24h_tokens : null}
        total={item.estimated_24h_budget_tokens}
        remaining={item.estimated_24h_remaining}
        source={item.estimated_24h_basis === 'same_known_plan_and_request_path_reference' ? t('modelUsageBudgetStrongReference') : item.estimated_24h_basis === 'unverified_plan_same_region_model_reference' ? t('modelUsageBudgetWeakReference') : item.estimated_24h_basis === 'own_complete_reset_window_reference' ? t('modelUsageBudgetBasis6004') : t('modelUsageBudgetBasisUnknown')}
        sampleRange={item.observed_24h_from && item.observed_24h_to ? formatDate(item.observed_24h_from, lang) + ' – ' + formatDate(item.observed_24h_to, lang) : t('modelUsageBudgetProgressUnavailable')}
        updatedAt={item.estimated_at ? formatDate(item.estimated_at, lang) : t('modelUsageBudgetProgressUnavailable')}
        usedLabel={t('modelUsageBudgetProgressUsed')}
        referenceLabel={t('modelUsageBudgetProgressReference')}
        remainingLabel={t('modelUsageBudgetProgressRemaining')}
        sampleRangeLabel={t('modelUsageBudgetProgressRange')}
        updatedLabel={t('modelUsageBudgetProgressUpdated')}
        exceedsReferenceLabel={t('modelUsageBudgetProgressExceeded')}
        unavailable={t('modelUsageBudgetProgressUnavailable')}
      />
    </div>
    {item.first_limit_24h_sample ? <div className="mt-2 rounded-lg border border-separator bg-surface p-2 text-xs"><div className="font-medium">{t('modelUsageBudgetFirstLimitSample')}</div><div className="mono mt-1">{item.first_limit_24h_sample.tokens == null ? t('modelUsageUnknown') : formatCount(item.first_limit_24h_sample.tokens)}</div><div className="mt-1 text-[10px] text-muted">{item.first_limit_24h_sample.from ? formatDate(item.first_limit_24h_sample.from, lang) : '—'} – {item.first_limit_24h_sample.to ? formatDate(item.first_limit_24h_sample.to, lang) : '—'} · {t(budget24hCoverageKey(item.first_limit_24h_sample.coverage))}{item.first_limit_24h_sample.missing_attempts ? ` · ${t('modelUsageBudgetMissing24h', { count: item.first_limit_24h_sample.missing_attempts })}` : ''} · {item.first_limit_24h_sample.confidence}</div></div> : null}
    {item.weak_references?.length ? <div className="mt-2 rounded-lg border border-separator bg-surface p-2 text-xs"><div className="font-medium">{t('modelUsageBudgetWeakReference')}</div>{item.weak_references.map((reference, index) => <div key={`${reference.scope}/${reference.request_path}/${index}`} className="mt-1 text-muted">{reference.request_path ? budgetPathLabel(reference.request_path, t) : t('modelUsageRequestPathUnknown')} · {reference.estimated_tokens == null ? t('modelUsageUnknown') : formatCount(reference.estimated_tokens)} · {reference.account_count} {t('modelUsageBudgetAccountsLabel')} / {reference.sample_count} {t('modelUsageBudgetWindowsLabel')} · {reference.confidence === 'single_complete_window' ? t('modelUsageBudgetSingleWindow') : t('modelUsageBudgetRepeatedWindows')}</div>)}{item.weak_references_truncated ? <div className="mt-1 text-warning">{t('modelUsageBudgetReferenceListTruncated')}</div> : null}</div> : null}
    {item.low_confidence_references?.length ? <div className="mt-2 rounded-lg border border-separator bg-surface p-2 text-xs"><div className="font-medium">{t('modelUsageBudgetReferenceSamples')}</div>{item.low_confidence_references.map((reference, index) => <div key={`${reference.account_id}/${reference.sample.from || ''}/${index}`} className="mt-1 text-muted">{accountNames.get(reference.account_id) || reference.account_id} · {reference.plan || t('modelUsagePlanUnknown')} · {reference.request_path ? budgetPathLabel(reference.request_path, t) : t('modelUsageRequestPathUnknown')} · {t(budgetSampleScopeKey(reference.scope))} · {reference.sample.tokens == null ? t('modelUsageUnknown') : formatCount(reference.sample.tokens)} · {t(budget24hCoverageKey(reference.sample.coverage))}</div>)}{item.low_confidence_references_truncated ? <div className="mt-1 text-warning">{t('modelUsageBudgetReferenceListTruncated')}</div> : null}</div> : null}
    <BudgetGuardEditor item={item} lang={lang} t={t} />
  </div>
}

function BudgetGuardEditor({ item, lang, t }: { item: BudgetEstimate; lang: 'zh' | 'en'; t: (key: string, vars?: Record<string, string | number>) => string }) {
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
      if (hasModelUsageLimit(current, item.model)) setValue(String(current.model_daily_tokens[modelKey]))
      else {
        const suggestion = safeBudgetGuardSuggestion(item)
        setValue(suggestion == null ? '' : String(suggestion))
      }
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

  return <div className="mt-3 border-t border-separator pt-3">
    {!open ? <Button size="sm" variant="secondary" onPress={() => void openEditor()}>{t('modelUsageGuardApplyAction')}</Button> : <div className="space-y-2 rounded-lg border border-separator bg-surface p-3">
      <div className="text-xs font-medium">{t('modelUsageGuardApplyTitle')}</div><p className="text-[10px] leading-5 text-muted">{t('modelUsageGuardApplyHint')}</p>
      {loading ? <p className="text-xs text-muted">{t('loading')}</p> : policy ? <>
        {configured ? <p className="text-xs text-warning">{t('modelUsageGuardExisting')} · {value || '0'}</p> : <>
          <Label className="text-xs">{t('modelUsageGuardTokenLimit')}</Label>
          <Input type="number" min={1} step={1} inputMode="numeric" value={value} aria-label={t('modelUsageGuardTokenLimit')} onChange={(event) => { setValue(event.target.value); setError(''); setSaved(false) }} />
          <p className="text-[10px] leading-5 text-muted">{safeBudgetGuardSuggestion(item) != null ? t('modelUsageGuardSafePrefill') : t('modelUsageGuardManualOnly')}</p>
        </>}
        <p className="text-[10px] text-muted">{t('modelUsageGuardResetAt', { time: formatBeijingDate(nextBeijingMidnight(), lang) })}</p>
      </> : null}
      {error ? <p role="alert" className="text-xs text-danger">{error}</p> : null}
      {saved ? <p role="status" className="text-xs text-success">{t('modelUsageGuardSaved')}</p> : null}
      <div className="flex flex-wrap gap-2"><Button size="sm" isPending={saving} isDisabled={loading || saving || configured || !policy} onPress={() => void save()}>{t('modelUsageGuardSave')}</Button><Button size="sm" variant="ghost" isDisabled={saving} onPress={() => { setOpen(false); setError(''); setSaved(false) }}>{t('cancel')}</Button></div>
    </div>}
  </div>
}

function BudgetValue({ label, value }: { label: string; value: string }) {
  return <div><div className="text-muted">{label}</div><div className="mono mt-1">{value}</div></div>
}

function BudgetProgress({ title, used, total, remaining, source, sampleRange, updatedAt, usedLabel, referenceLabel, remainingLabel, sampleRangeLabel, updatedLabel, exceedsReferenceLabel, unavailable }: { title: string; used: number | null | undefined; total: number | null | undefined; remaining: number | null | undefined; source: string; sampleRange: string; updatedAt: string; usedLabel: string; referenceLabel: string; remainingLabel: string; sampleRangeLabel: string; updatedLabel: string; exceedsReferenceLabel: string; unavailable: string }) {
  const percentage = budgetProgressPercent(used, total)
  const exceeded = budgetProgressExceedsReference(used, total)
  const valueLabel = [title, usedLabel + ': ' + (used == null ? unavailable : formatCount(used)), referenceLabel + ': ' + (total == null ? unavailable : formatCount(total)), remainingLabel + ': ' + (remaining == null ? unavailable : formatCount(remaining)), source, sampleRange, updatedAt, exceeded ? exceedsReferenceLabel : ''].filter(Boolean).join(' · ')
  return <div className="rounded-lg border border-separator bg-surface p-2 text-xs">
    <div className="flex flex-wrap items-center justify-between gap-1"><span className="font-medium">{title}</span><span className="mono text-muted">{percentage == null ? unavailable : String(percentage) + '%'}{exceeded ? ' · ' + exceedsReferenceLabel : ''}</span></div>
    {percentage != null ? <Meter className="mt-2 w-full" size="sm" minValue={0} maxValue={100} value={percentage} aria-label={title} valueLabel={valueLabel} color={exceeded ? 'warning' : 'default'}><Meter.Track><Meter.Fill /></Meter.Track></Meter> : <div className="mt-2 text-[10px] text-muted">{unavailable}</div>}
    <div className="mt-2 flex flex-wrap justify-between gap-x-3 gap-y-1 text-[10px] text-muted"><span>{usedLabel}: {used == null ? unavailable : formatCount(used)} · {referenceLabel}: {total == null ? unavailable : formatCount(total)} · {source}</span><span>{remainingLabel}: {remaining == null ? unavailable : formatCount(remaining)}</span><span>{sampleRangeLabel}: {sampleRange}</span><span>{updatedLabel}: {updatedAt}</span></div>
  </div>
}

function ModelSelect({ label, value, onChange, options, placeholder }: { label: string; value: string; onChange: (value: string) => void; options: { id: string; label: string }[]; placeholder: string }) {
  const selected = options.find((option) => option.id === value)
  return <Select value={value || null} placeholder={placeholder} onChange={(next) => { if (typeof next === 'string') onChange(next) }}>
    <Label className="text-xs font-medium text-muted">{label}</Label>
    <Select.Trigger className="items-center"><Select.Value className="min-w-0 truncate">{({ defaultChildren, isPlaceholder }) => isPlaceholder || !selected ? defaultChildren : <span className="truncate">{selected.label}</span>}</Select.Value><Select.Indicator /></Select.Trigger>
    <Select.Popover className="max-h-72"><ListBox>{options.map((option) => <ListBox.Item key={option.id} id={option.id} textValue={option.label}><Label className="block truncate">{option.label}</Label><ListBox.ItemIndicator /></ListBox.Item>)}</ListBox></Select.Popover>
  </Select>
}
