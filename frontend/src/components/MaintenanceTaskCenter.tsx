import { useEffect, useState } from 'react'
import { Button, Card, Input, NumberField } from '@heroui/react'
import { api } from '@/api/client'
import { fetchMaintenanceLogs, type MaintenanceLog } from '@/api/logs'
import { maintenanceActionDisabled, quotaSnapshotStale } from '@/lib/maintenance'
import { updateSystemSettings, type MaintenanceSchedule, type SystemSettings } from '@/api/system'
import type { Overview } from '@/api/types'
import { CompactSwitch } from '@/components/ui/CompactSwitch'
import { FormRow } from '@/components/ui/FormRow'
import { PageAlert } from '@/components/ui/PageAlert'
import { useI18n } from '@/hooks/useI18n'

export function MaintenanceTaskCenter({ settings, onSaved, selected, onExecuted }: {
  settings: SystemSettings | null
  onSaved: (value: SystemSettings) => void
  selected?: NonNullable<Overview['accounts']>[number]
  onExecuted: () => void
}) {
  const { t } = useI18n()
  const [draft, setDraft] = useState<MaintenanceSchedule | null>(null)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [latestState, setLatestState] = useState<{ accountID?: string; entries: Record<string, MaintenanceLog | undefined> }>({ entries: {} })
  const latest = latestState.accountID === selected?.id ? latestState.entries : {}
  useEffect(() => {
    let current = true
    if (selected) void Promise.all(['credential', 'quota'].map(async (kind) => {
      const page = await fetchMaintenanceLogs({ account: selected.id, kind, limit: 1 })
      return [kind, page.items[0]] as const
    })).then((entries) => { if (current) setLatestState({ accountID: selected.id, entries: Object.fromEntries(entries) }) }).catch(() => undefined)
    return () => { current = false }
  }, [selected])
  const schedule = draft ?? settings?.maintenance_schedule
  function edit(patch: Partial<MaintenanceSchedule>) {
    if (schedule) setDraft({ ...schedule, ...patch })
  }
  async function save() {
    if (!draft) return
    setBusy('save'); setError('')
    try { onSaved(await updateSystemSettings({ maintenance_schedule: draft })); setDraft(null) }
    catch (err) { setError(err instanceof Error ? err.message : String(err)) }
    finally { setBusy('') }
  }
  async function run(action: string) {
    if (!selected) return
    setBusy(action); setError('')
    try { await api(`/api/accounts/${encodeURIComponent(selected.id)}/${action}`, { method: 'POST', body: '{}' }) }
    catch (err) { setError(err instanceof Error ? err.message : String(err)) }
    finally { setBusy(''); onExecuted() }
  }
  const stale = quotaSnapshotStale(selected?.quota?.fetched_at, settings?.quota_refresh_interval_minutes ?? 30)
  return <Card>
    <Card.Header><Card.Title>{t('maintenanceTaskCenter')}</Card.Title><Card.Description>{t('maintenanceTaskCenterHint')}</Card.Description></Card.Header>
    <Card.Content className="space-y-3">
      {error ? <PageAlert title={error} /> : null}
      <FormRow label={t('maintenanceKind_credential')} hint={t('maintenanceCredentialHint')}>
        <CompactSwitch isSelected={schedule?.credential_enabled ?? true} isDisabled={!schedule || Boolean(busy)} ariaLabel={t('maintenanceKind_credential')} onChange={(credential_enabled) => edit({ credential_enabled })} />
        <NumberField value={schedule?.credential_interval_minutes ?? 15} minValue={5} maxValue={1440} isDisabled={!schedule || Boolean(busy)} onChange={(value) => { if (Number.isInteger(value)) edit({ credential_interval_minutes: value }) }}>
          <NumberField.Group><NumberField.DecrementButton /><NumberField.Input aria-label={t('maintenanceCredentialInterval')} /><NumberField.IncrementButton /></NumberField.Group>
        </NumberField>
      </FormRow>
      <FormRow label={t('maintenanceKind_quota')} hint={t('maintenanceQuotaHint')}><CompactSwitch isSelected={schedule?.quota_enabled ?? true} isDisabled={!schedule || Boolean(busy)} ariaLabel={t('maintenanceKind_quota')} onChange={(quota_enabled) => edit({ quota_enabled })} /></FormRow>
      <FormRow label={t('maintenanceKind_daily_tasks')} hint={t('maintenanceDailyScheduleHint')}>
        <CompactSwitch isSelected={schedule?.daily_enabled ?? true} isDisabled={!schedule || Boolean(busy)} ariaLabel={t('maintenanceKind_daily_tasks')} onChange={(daily_enabled) => edit({ daily_enabled })} />
        <Input aria-label={t('maintenanceDailyTime')} value={schedule?.daily_time ?? '22:00'} disabled={!schedule || Boolean(busy)} onChange={(event) => edit({ daily_time: event.target.value })} />
      </FormRow>
      <div className="flex justify-end"><Button size="sm" variant="secondary" isDisabled={!draft || Boolean(busy)} isPending={busy === 'save'} onPress={() => void save()}>{t('save')}</Button></div>
      <p className="text-xs text-muted">{selected ? `${t('logsColAccount')}: ${selected.name || selected.id}` : t('maintenanceSelectAccount')}</p>
      {selected ? <div className="space-y-1 text-xs text-muted">
        {(['credential', 'quota'] as const).map((kind) => <p key={kind}>{t(`maintenanceKind_${kind}`)}: {latest[kind] ? `${t(`dailyStatus_${latest[kind]?.status}`)} · ${t('maintenanceNextExecution')}: ${latest[kind]?.next_retry_at ? new Date(latest[kind]!.next_retry_at!).toLocaleString() : t('logsUnknown')}` : t('logsUnknown')}</p>)}
        <p>{t('maintenanceQuotaSnapshot')}: {selected.quota?.fetched_at ? new Date(selected.quota.fetched_at).toLocaleString() : t('logsUnknown')} · {stale === null ? t('logsUnknown') : stale || latest.quota?.status === 'error' ? t('maintenanceQuotaStale') : t('maintenanceQuotaFresh')}</p>
      </div> : null}
      <div className="flex flex-wrap gap-2">
        {(['maintain', 'refresh?quota=1', 'checkin', 'daily-tasks'] as const).map((action) => <Button key={action} size="sm" variant="secondary" isDisabled={maintenanceActionDisabled(selected, action, busy)} isPending={busy === action} onPress={() => void run(action)}>{t(action === 'maintain' ? 'maintenanceRunCredential' : action === 'refresh?quota=1' ? 'maintenanceRunQuota' : action === 'checkin' ? 'maintenanceRunCheckin' : 'maintenanceRunDaily')}</Button>)}
      </div>
    </Card.Content>
  </Card>
}
