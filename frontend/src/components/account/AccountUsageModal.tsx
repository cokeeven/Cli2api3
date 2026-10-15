import { useEffect, useState } from 'react'
import { Alert, Button, Chip, Form, Input, Label, Modal, NumberField, Table } from '@heroui/react'
import { ArrowClockwise, Plus, TrashSimple, X } from '@phosphor-icons/react'
import { fetchAccountUsage, reconcileAccountUsage, updateAccountUsagePolicy, type AccountUsage, type AccountUsagePolicy } from '@/api/usage'
import { refreshAccount } from '@/api/overview'
import { ConfirmDialog } from '@/components/ui/ConfirmDialog'
import { CompactSwitch } from '@/components/ui/CompactSwitch'
import { FilterToggle } from '@/components/ui/FilterToggle'
import { FormRow } from '@/components/ui/FormRow'
import { PageAlert } from '@/components/ui/PageAlert'
import { SkeletonBlock } from '@/components/ui/PageSkeletons'
import { formatQuotaAmount, type AccountRow } from '@/lib/account'
import { usagePolicyWithModels, validateUsagePolicy, type ModelTokenLimit } from '@/lib/usagePolicy'
import { UsageAnalysisDetails } from './UsageAnalysisDetails'

type Translate = (key: string, vars?: Record<string, string | number>) => string
const EMPTY_POLICY: AccountUsagePolicy = { reserve_credits: 0, daily_tokens: 0, daily_credits: 0, model_daily_tokens: {}, prefer_expiring: false }

function timeLabel(value?: string | number) {
  if (!value) return '—'
  const date = new Date(typeof value === 'number' ? value * 1000 : value)
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString()
}

export function AccountUsageModal({ account, t, onClose }: { account: AccountRow; t: Translate; onClose: () => void }) {
  const [usage, setUsage] = useState<AccountUsage | null>(null)
  const [policy, setPolicy] = useState<AccountUsagePolicy>(EMPTY_POLICY)
  const [models, setModels] = useState<ModelTokenLimit[]>([])
  const [days, setDays] = useState(7)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const [reconcileOpen, setReconcileOpen] = useState(false)
  const [reconcileNotice, setReconcileNotice] = useState('')
  const [refreshingBalance, setRefreshingBalance] = useState(false)

  async function load(nextDays = days) {
    setLoading(true)
    setError('')
    try {
      const result = await fetchAccountUsage(account.id, nextDays)
      setUsage(result)
      if (!usage) {
        setPolicy(result.policy)
        setModels(Object.entries(result.policy.model_daily_tokens || {}).filter(([, tokens]) => tokens > 0).map(([model, tokens]) => ({ model, tokens })))
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally { setLoading(false) }
  }

  useEffect(() => {
    let active = true
    void fetchAccountUsage(account.id, 7).then((result) => {
      if (!active) return
      setUsage(result)
      setPolicy(result.policy)
      setModels(Object.entries(result.policy.model_daily_tokens || {}).filter(([, tokens]) => tokens > 0).map(([model, tokens]) => ({ model, tokens })))
    }).catch((err) => {
      if (active) setError(err instanceof Error ? err.message : String(err))
    }).finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [account.id])

  function changePolicy(next: AccountUsagePolicy) {
    setPolicy(next)
    setSaved(false)
  }
  function changeModels(next: ModelTokenLimit[]) {
    setModels(next)
    setSaved(false)
  }
  async function save(event?: { preventDefault(): void }) {
    event?.preventDefault()
    const invalid = validateUsagePolicy(policy, models)
    if (invalid) { setError(t(invalid)); return }
    setSaving(true)
    setError('')
    setSaved(false)
    try {
      const next = usagePolicyWithModels(policy, models)
      await updateAccountUsagePolicy(account.id, next)
      setPolicy(next)
      setSaved(true)
    } catch (err) { setError(err instanceof Error ? err.message : String(err)) }
    finally { setSaving(false) }
  }

  async function reconcile() {
    if (!usage?.quota.fetched_at) return
    setSaving(true)
    setError('')
    try {
      const result = await reconcileAccountUsage(account.id, usage.quota.fetched_at)
      setReconcileOpen(false)
      setReconcileNotice(t('usageReconciled', { count: result.covered_attempts }))
      await load()
    } catch (err) { setReconcileOpen(false); setError(err instanceof Error ? err.message : String(err)) }
    finally { setSaving(false) }
  }

  async function refreshBalance() {
    setRefreshingBalance(true)
    setError('')
    try { await refreshAccount(account.id, { quota: true }); await load() }
    catch (err) { setError(err instanceof Error ? err.message : String(err)) }
    finally { setRefreshingBalance(false) }
  }

  return <Modal.Root isOpen onOpenChange={(open: boolean) => { if (!open && !saving) onClose() }}>
    <Modal.Backdrop variant="blur">
      <Modal.Container size="lg" placement="center" scroll="inside">
        <Modal.Dialog className="sm:min-w-[40rem]">
          <Modal.Header className="items-start justify-between gap-4 px-6 pt-6">
            <div><Modal.Heading className="text-lg font-semibold">{t('usageTitle', { name: account.name || account.id })}</Modal.Heading><p className="mt-1 text-xs leading-5 text-muted">{t('usageScopeHint')}</p></div>
            <Modal.CloseTrigger isDisabled={saving} aria-label={t('close')} className="grid size-9 shrink-0 place-items-center rounded-lg text-muted hover:bg-surface-secondary"><X size={18} /></Modal.CloseTrigger>
          </Modal.Header>
          <Modal.Body className="space-y-5 px-6 pb-2">
            {error ? <PageAlert title={error} /> : null}
            {saved ? <Alert status="success"><Alert.Indicator /><Alert.Content><Alert.Title>{t('usageSaved')}</Alert.Title></Alert.Content></Alert> : null}
            {reconcileNotice ? <Alert status="success"><Alert.Indicator /><Alert.Content><Alert.Title>{reconcileNotice}</Alert.Title></Alert.Content></Alert> : null}
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="text-sm font-medium">{t('usageActualOverview')}</div>
              <div className="flex items-center gap-2">
                <FilterToggle value={String(days)} ariaLabel={t('usageWindow')} options={[{ id: '7', label: t('usage7d') }, { id: '30', label: t('usage30d') }]} onChange={(value) => { if (loading || saving) return; const next = Number(value); setDays(next); void load(next) }} />
                <Button size="sm" isIconOnly variant="ghost" isDisabled={loading || saving} aria-label={t('refresh')} onPress={() => void load()}><ArrowClockwise size={16} /></Button>
              </div>
            </div>
            {loading ? <div className="space-y-3" aria-busy><SkeletonBlock className="h-24 w-full" /><SkeletonBlock className="h-44 w-full" /></div> : usage ? <>
              <dl className="grid grid-cols-2 gap-3 rounded-lg bg-surface-secondary p-3 sm:grid-cols-3">
                {[
                  [t('usageAvailable'), usage.quota.known ? `${formatQuotaAmount(usage.quota.available)} ${usage.quota.unit || ''}`.trim() : t('usageUnknown')],
                  [t('usageTodayTokens'), formatQuotaAmount(usage.today.tokens_actual)],
                  [t('usageTodayCredits'), formatQuotaAmount(usage.today.credits_actual)],
                  [t('usageReservedTokens'), formatQuotaAmount(usage.today.tokens_reserved)],
                  [t('usageReservedCredits'), formatQuotaAmount(usage.today.credits_reserved)],
                  [t('usageUnknownAttempts'), String(usage.today.unknown_attempts ?? 0)],
                ].map(([label, value]) => <div key={label}><dt className="text-[11px] text-muted">{label}</dt><dd className="mono mt-1 text-base font-medium">{value}</dd></div>)}
              </dl>
              <p className="text-xs leading-5 text-muted">{t('usageAccuracyHint', { estimated: usage.today.estimated_attempts ?? 0, unknown: usage.today.unknown_attempts ?? 0 })}</p>
              {usage.quota.unit === 'credits' && ((usage.credit_balance_pending ?? 0) > 0 || (usage.credit_balance_unbounded ?? 0) > 0) ? <section className="space-y-2">
                <p className="text-xs leading-5 text-muted">{t('usagePendingBalance', { amount: formatQuotaAmount(usage.credit_balance_pending), count: usage.credit_balance_unbounded ?? 0 })}</p>
                <Button size="sm" variant="secondary" isDisabled={!usage.quota.known || !usage.quota.fetched_at || saving || refreshingBalance} onPress={() => setReconcileOpen(true)}>{t('usageReconcileAction')}</Button>
              </section> : null}
              <section className="space-y-2">
                <div className="flex flex-wrap items-center justify-between gap-2"><h3 className="text-sm font-medium">{t('usageExpiryTitle')}</h3><Button size="sm" variant="ghost" isPending={refreshingBalance} isDisabled={saving || loading} onPress={() => void refreshBalance()}>{t('usageRefreshBalance')}</Button></div>
                <div className="flex flex-wrap gap-2">
                  <Chip size="sm" variant="soft" color="warning">{t('usageExpiry7d', { amount: usage.quota.known ? `${formatQuotaAmount(usage.quota.expiring_7d)} ${usage.quota.unit || ''}`.trim() : '—' })}</Chip>
                  <Chip size="sm" variant="soft">{t('usageExpiry30d', { amount: usage.quota.known ? `${formatQuotaAmount(usage.quota.expiring_30d)} ${usage.quota.unit || ''}`.trim() : '—' })}</Chip>
                </div>
                <p className="text-[11px] leading-5 text-muted">{t('usageExpiryUnknown', { amount: usage.quota.known ? `${formatQuotaAmount(usage.quota.unknown_expiry)} ${usage.quota.unit || ''}`.trim() : '—', time: timeLabel(usage.quota.fetched_at) })}</p>
                {usage.quota.packages?.length ? <Table><Table.ScrollContainer><Table.Content aria-label={t('usagePackages')}>
                  <Table.Header><Table.Column isRowHeader>{t('usageSource')}</Table.Column><Table.Column>{t('quotaRemaining')}</Table.Column><Table.Column>{t('usageExpiry')}</Table.Column><Table.Column>{t('logsColStatus')}</Table.Column></Table.Header>
                  <Table.Body>{usage.quota.packages.map((pack, index) => <Table.Row key={`${pack.id || 'package'}-${index}`}>
                    <Table.Cell><span className="text-xs">{pack.source || t('usageUnknown')}</span></Table.Cell><Table.Cell><span className="mono text-xs">{formatQuotaAmount(pack.remain)} {pack.unit || usage.quota.unit || ''}</span></Table.Cell><Table.Cell><span className="mono text-[11px]">{timeLabel(pack.ends_at || pack.end_time)}</span>{pack.expiry_source === 'cycle' ? <p className="mt-1 text-[11px] text-muted">{t('usageCycleBoundary')}</p> : null}</Table.Cell><Table.Cell><span className="text-xs">{pack.status === 'expired' ? t('usageExpired') : pack.status === 'exhausted' ? t('usageExhausted') : pack.status === 'inactive' ? t('usageInactive') : pack.status === 'active' ? t('usageActive') : t('usageUnknown')}</span></Table.Cell>
                  </Table.Row>)}</Table.Body>
                </Table.Content></Table.ScrollContainer></Table> : null}
              </section>
              <Table><Table.ScrollContainer><Table.Content aria-label={t('usageHistory')}>
                <Table.Header><Table.Column isRowHeader>{t('usageDay')}</Table.Column><Table.Column>{t('usageTokensActual')}</Table.Column><Table.Column>{t('usageCreditsActual')}</Table.Column><Table.Column>{t('usageEstimated')}</Table.Column><Table.Column>{t('usageUnknown')}</Table.Column></Table.Header>
                <Table.Body>{usage.series.map((row) => <Table.Row key={row.day}><Table.Cell><span className="mono text-xs">{row.day}</span></Table.Cell><Table.Cell><span className="mono text-xs">{formatQuotaAmount(row.tokens_actual)}</span></Table.Cell><Table.Cell><span className="mono text-xs">{formatQuotaAmount(row.credits_actual)}</span></Table.Cell><Table.Cell><span className="mono text-xs">{row.estimated_attempts}</span></Table.Cell><Table.Cell><span className="mono text-xs">{row.unknown_attempts}</span></Table.Cell></Table.Row>)}</Table.Body>
              </Table.Content></Table.ScrollContainer></Table>
              <p className="text-[11px] leading-5 text-muted">{usage.recording_since ? t('usageRecordingSince', { time: timeLabel(usage.recording_since) }) : t('usageNotRecorded')}</p>
              <UsageAnalysisDetails usage={usage} t={t} />
            </> : null}
            <Form className="space-y-4 border-t border-separator pt-4" onSubmit={(event) => void save(event)}>
              <div><h3 className="text-sm font-medium">{t('usageGuardsTitle')}</h3><p className="mt-1 text-xs leading-5 text-muted">{t('usageGuardsHint')}</p></div>
              {(['reserve_credits', 'daily_tokens', 'daily_credits'] as const).map((field) => {
                const labels = { reserve_credits: 'usageReserveGuard', daily_tokens: 'usageDailyTokenGuard', daily_credits: 'usageDailyCreditGuard' }
                return <FormRow key={field} label={t(labels[field])}>
                  <NumberField minValue={0} value={policy[field]} isDisabled={loading || saving || !usage} onChange={(value) => changePolicy({ ...policy, [field]: Number.isFinite(value) ? value : 0 })} formatOptions={field === 'daily_tokens' ? { maximumFractionDigits: 0 } : { maximumFractionDigits: 4 }}>
                    <NumberField.Group><NumberField.DecrementButton /><NumberField.Input aria-label={t(labels[field])} /><NumberField.IncrementButton /></NumberField.Group>
                  </NumberField>
                </FormRow>
              })}
              <section className="space-y-2">
                <Label className="text-sm font-medium">{t('usageModelGuard')}</Label>
                <p className="text-xs leading-5 text-muted">{t('usageModelGuardHint')}</p>
                {models.map((row, index) => <div key={index} className="flex items-center gap-2">
                  <Input className="min-w-0 flex-1" value={row.model} disabled={loading || saving} aria-label={t('usageModelID')} placeholder={t('usageModelID')} onChange={(event) => changeModels(models.map((item, i) => i === index ? { ...item, model: event.target.value } : item))} />
                  <NumberField className="w-36 shrink-0" minValue={1} value={row.tokens} isDisabled={loading || saving} onChange={(value) => changeModels(models.map((item, i) => i === index ? { ...item, tokens: value ?? 0 } : item))}>
                    <NumberField.Group><NumberField.Input aria-label={t('usageDailyTokenGuard')} /></NumberField.Group>
                  </NumberField>
                  <Button size="sm" isIconOnly variant="ghost" isDisabled={loading || saving} aria-label={t('delete')} onPress={() => changeModels(models.filter((_, i) => i !== index))}><TrashSimple size={15} /></Button>
                </div>)}
                <Button size="sm" variant="secondary" isDisabled={loading || saving || !usage} onPress={() => changeModels([...models, { model: '', tokens: 10000 }])}><Plus size={14} />{t('usageAddModel')}</Button>
              </section>
              <FormRow label={t('usagePreferExpiring')} hint={t('usagePreferExpiringHint')}><CompactSwitch isSelected={policy.prefer_expiring} isDisabled={loading || saving || !usage} ariaLabel={t('usagePreferExpiring')} onChange={(value) => changePolicy({ ...policy, prefer_expiring: value })} /></FormRow>
              <p className="text-xs leading-5 text-muted">{t('usageCostGuardHint')}</p>
            </Form>
          </Modal.Body>
          <Modal.Footer className="justify-end"><Button variant="ghost" isDisabled={saving} onPress={onClose}>{t('close')}</Button><Button isPending={saving} isDisabled={loading || !usage} onPress={() => void save()}>{t('usageSavePolicy')}</Button></Modal.Footer>
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
    <ConfirmDialog
      isOpen={reconcileOpen}
      title={t('usageReconcileAction')}
      description={t('usageReconcileHint')}
      confirmLabel={t('usageReconcileConfirm')}
      cancelLabel={t('cancel')}
      closeLabel={t('close')}
      isPending={saving}
      status="warning"
      confirmVariant="primary"
      onClose={() => { if (!saving) setReconcileOpen(false) }}
      onConfirm={() => void reconcile()}
    />
  </Modal.Root>
}
