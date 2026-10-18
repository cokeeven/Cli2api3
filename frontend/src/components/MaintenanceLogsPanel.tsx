import { useEffect, useMemo, useState } from 'react'
import { getLocalTimeZone, type DateValue } from '@internationalized/date'
import { Button, Card, Chip, DateField, DateRangePicker, Modal, RangeCalendar, Table } from '@heroui/react'
import { ArrowClockwise, CalendarCheck } from '@phosphor-icons/react'
import { fetchMaintenanceLogs, type MaintenanceLog } from '@/api/logs'
import { fetchAccounts } from '@/api/overview'
import { fetchSystemSettings, updateSystemSettings, type SystemSettings } from '@/api/system'
import type { Overview } from '@/api/types'
import { dailyStatusColor, dailyStepLabel } from '@/lib/dailyTasks'
import { CompactSwitch } from '@/components/ui/CompactSwitch'
import { EmptyPanel } from '@/components/ui/EmptyPanel'
import { FilterSearchSelect } from '@/components/ui/FilterSearchSelect'
import { FilterSelect } from '@/components/ui/FilterSelect'
import { FilterToggle } from '@/components/ui/FilterToggle'
import { ListPager, type PageSize } from '@/components/ui/ListPager'
import { PageAlert } from '@/components/ui/PageAlert'
import { LogsRequestListSkeleton, SkeletonBlock } from '@/components/ui/PageSkeletons'
import { useI18n } from '@/hooks/useI18n'
import { accountProviderFamilyLabel, accountProviderLabel } from '@/lib/provider'
import { MaintenanceTaskCenter } from '@/components/MaintenanceTaskCenter'

type DateRange = { start: DateValue; end: DateValue }
type RangePreset = 'all' | '1h' | '24h' | '7d' | 'custom'

export function MaintenanceLogsPanel() {
  const { t, lang } = useI18n()
  const [settings, setSettings] = useState<SystemSettings | null>(null)
  const [settingsLoading, setSettingsLoading] = useState(true)
  const [settingsError, setSettingsError] = useState('')
  const [saving, setSaving] = useState(false)
  const [accounts, setAccounts] = useState<NonNullable<Overview['accounts']>>([])
  const [items, setItems] = useState<MaintenanceLog[]>([])
  const [total, setTotal] = useState(0)
  const [loadedKey, setLoadedKey] = useState('')
  const [error, setError] = useState('')
  const [account, setAccount] = useState('')
  const [provider, setProvider] = useState('')
  const [kind, setKind] = useState('')
  const [status, setStatus] = useState('')
  const [trigger, setTrigger] = useState('')
  const [range, setRange] = useState<RangePreset>('all')
  const [customRange, setCustomRange] = useState<DateRange | null>(null)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState<PageSize>(20)
  const [refresh, setRefresh] = useState(0)
  const [selected, setSelected] = useState<MaintenanceLog | null>(null)

  useEffect(() => {
    let current = true
    void fetchSystemSettings().then((value) => { if (current) { setSettings(value); setSettingsError('') } })
      .catch((err: unknown) => { if (current) setSettingsError(err instanceof Error ? err.message : String(err)) })
      .finally(() => { if (current) setSettingsLoading(false) })
    void fetchAccounts(false).then((value) => { if (current) setAccounts(value.data || []) }).catch(() => undefined)
    return () => { current = false }
  }, [refresh])

  const filterKey = [account, provider, kind, status, trigger, range, customRange?.start.toString(), customRange?.end.toString(), pageSize].join('\0')
  const [appliedFilter, setAppliedFilter] = useState(filterKey)
  if (appliedFilter !== filterKey) { setAppliedFilter(filterKey); setPage(1) }
  const pageCount = Math.max(1, Math.ceil(total / pageSize))
  const currentPage = Math.min(page, pageCount)
  if (page !== currentPage) setPage(currentPage)
  const requestKey = [filterKey, currentPage, refresh].join('\0')
  const loading = loadedKey !== requestKey

  useEffect(() => {
    let current = true
    let from: string | undefined
    let to: string | undefined
    if (range === 'custom' && customRange) {
      from = customRange.start.toDate(getLocalTimeZone()).toISOString()
      const end = customRange.end.toDate(getLocalTimeZone())
      end.setHours(23, 59, 59, 999)
      to = end.toISOString()
    } else if (range !== 'all' && range !== 'custom') {
      const hours = range === '1h' ? 1 : range === '24h' ? 24 : 168
      from = new Date(Date.now() - hours * 3600000).toISOString()
      to = new Date().toISOString()
    }
    void fetchMaintenanceLogs({ account, provider, kind, status, trigger, from, to, limit: pageSize, offset: (currentPage - 1) * pageSize })
      .then((value) => { if (current) { setItems(value.items || []); setTotal(value.total); setError('') } })
      .catch((err: unknown) => { if (current) setError(err instanceof Error ? err.message : String(err)) })
      .finally(() => { if (current) setLoadedKey(requestKey) })
    return () => { current = false }
  }, [account, provider, kind, status, trigger, range, customRange, currentPage, pageSize, refresh, requestKey])

  const providerOptions = useMemo(() => {
    const providers = new Map<string, string>()
    for (const row of accounts) if (row.provider) providers.set(row.provider, accountProviderFamilyLabel(row.provider, t))
    return [...providers].map(([id, label]) => ({ id, label }))
  }, [accounts, t])

  async function toggleAutomation(value: boolean) {
    setSaving(true); setSettingsError('')
    try { setSettings(await updateSystemSettings({ automation_maintenance_enabled: value })) }
    catch (err) { setSettingsError(err instanceof Error ? err.message : String(err)) }
    finally { setSaving(false) }
  }
  function formatTime(value?: string) {
    if (!value) return '—'
    const date = new Date(value)
    return Number.isFinite(date.getTime()) ? date.toLocaleString(lang === 'zh' ? 'zh-CN' : 'en-US', { hour12: false }) : value
  }
  function clearFilters() {
    setAccount(''); setProvider(''); setKind(''); setStatus(''); setTrigger(''); setRange('all'); setCustomRange(null); setPage(1)
  }
  const hasFilters = Boolean(account || provider || kind || status || trigger || range !== 'all')
  return (
    <div className="space-y-4">
      <Card>
        <Card.Header className="flex-row flex-wrap items-start justify-between gap-3">
          <div><Card.Title>{t('maintenanceAutomation')}</Card.Title><Card.Description className="mt-1 max-w-3xl">{t('maintenanceAutomationHint')}</Card.Description></div>
          {settingsLoading && !settings ? <SkeletonBlock className="h-7 w-20" /> : <CompactSwitch isSelected={settings?.automation_maintenance_enabled ?? true} isDisabled={!settings || saving || settingsLoading} ariaLabel={t('maintenanceAutomation')} onChange={(value) => void toggleAutomation(value)} />}
        </Card.Header>
        {settingsError ? <Card.Content><PageAlert title={settingsError} /></Card.Content> : settings?.automation_maintenance_enabled === false ? <Card.Content><PageAlert status="default" title={t('maintenancePaused')} description={t('maintenanceManualHint')} /></Card.Content> : null}
      </Card>

      <MaintenanceTaskCenter settings={settings} onSaved={setSettings} selected={accounts.find((row) => row.id === account)} onExecuted={() => setRefresh((value) => value + 1)} />

      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="max-w-3xl text-sm text-muted">{t('maintenanceLogsHint')}</p>
        <Button size="sm" variant="secondary" isPending={loading} isDisabled={saving} onPress={() => { setSettingsLoading(true); setRefresh((value) => value + 1) }}><ArrowClockwise size={15} />{t('refresh')}</Button>
      </div>
      <div className="flex flex-wrap gap-2">
        <FilterSelect ariaLabel={t('logsColProvider')} value={provider} onChange={setProvider} options={[{ id: '', label: t('maintenanceAllProviders') }, ...providerOptions]} />
        <FilterSearchSelect ariaLabel={t('logsColAccount')} value={account} onChange={setAccount} options={accounts.map((row) => ({ id: row.id, label: row.name || row.id }))} allLabel={t('logsFilterAccountAll')} searchPlaceholder={t('logsSearchAccount')} emptyLabel={t('logsNoFilterOptions')} />
        <FilterSelect ariaLabel={t('maintenanceKind')} value={kind} onChange={setKind} options={[{ id: '', label: t('maintenanceAllKinds') }, ...['checkin', 'daily_tasks', 'credential', 'quota'].map((id) => ({ id, label: t(`maintenanceKind_${id}`) }))]} />
        <FilterSelect ariaLabel={t('logsColStatus')} value={status} onChange={setStatus} options={[{ id: '', label: t('logsFilterAll') }, ...['success', 'already', 'partial', 'error', 'skipped', 'running', 'interrupted'].map((id) => ({ id, label: t(`dailyStatus_${id}`) }))]} />
        <FilterSelect ariaLabel={t('maintenanceTrigger')} value={trigger} onChange={setTrigger} options={[{ id: '', label: t('maintenanceAllTriggers') }, ...['manual', 'scheduled', 'retry', 'unknown'].map((id) => ({ id, label: t(`dailyTrigger_${id}`) }))]} />
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <FilterToggle value={range} onChange={(value) => setRange(value as RangePreset)} ariaLabel={t('logsTimeRange')} options={['all', '1h', '24h', '7d', 'custom'].map((id) => ({ id, label: t(`logsTime${id === 'all' ? 'All' : id === 'custom' ? 'Custom' : id}`) }))} />
        {range === 'custom' ? <DateRangePicker value={customRange} onChange={setCustomRange} aria-label={t('logsTimeRange')} className="w-[min(100%,26rem)]">
          <DateField.Group variant="secondary"><DateField.Input slot="start" aria-label={t('logsTimeFrom')}>{(segment) => <DateField.Segment segment={segment} />}</DateField.Input><DateRangePicker.RangeSeparator /><DateField.Input slot="end" aria-label={t('logsTimeTo')}>{(segment) => <DateField.Segment segment={segment} />}</DateField.Input><DateField.Suffix><DateRangePicker.Trigger><DateRangePicker.TriggerIndicator /></DateRangePicker.Trigger></DateField.Suffix></DateField.Group>
          <DateRangePicker.Popover><RangeCalendar aria-label={t('logsTimeRange')}><RangeCalendar.Header><RangeCalendar.YearPickerTrigger><RangeCalendar.YearPickerTriggerHeading /><RangeCalendar.YearPickerTriggerIndicator /></RangeCalendar.YearPickerTrigger><RangeCalendar.NavButton slot="previous" /><RangeCalendar.NavButton slot="next" /></RangeCalendar.Header><RangeCalendar.Grid><RangeCalendar.GridHeader>{(day) => <RangeCalendar.HeaderCell>{day}</RangeCalendar.HeaderCell>}</RangeCalendar.GridHeader><RangeCalendar.GridBody>{(date) => <RangeCalendar.Cell date={date} />}</RangeCalendar.GridBody></RangeCalendar.Grid><RangeCalendar.YearPickerGrid><RangeCalendar.YearPickerGridBody>{({ year }) => <RangeCalendar.YearPickerCell year={year} />}</RangeCalendar.YearPickerGridBody></RangeCalendar.YearPickerGrid></RangeCalendar></DateRangePicker.Popover>
        </DateRangePicker> : null}
        {hasFilters ? <Button size="sm" variant="ghost" onPress={clearFilters}>{t('clearFilters')}</Button> : null}
        <span className="mono text-[11px] text-muted">{t('logsShownTotal', { shown: total ? `${(currentPage - 1) * pageSize + 1}–${Math.min(currentPage * pageSize, total)}` : 0, total })}</span>
      </div>
      {error ? <PageAlert title={t('failedLogs', { msg: error })} /> : null}
      <MaintenanceLogList items={items} loading={loading} onSelect={setSelected} formatTime={formatTime} />
      <ListPager total={total} page={currentPage} pageCount={pageCount} pageSize={pageSize} loading={loading} pageSizeLabel={t('logsPageSize')} pageLabel={t('logsPage', { page: currentPage, pages: pageCount })} prevLabel={t('logsPrevPage')} nextLabel={t('logsNextPage')} onPage={setPage} onPageSize={setPageSize} />
      <Modal.Root isOpen={Boolean(selected)} onOpenChange={(open: boolean) => { if (!open) setSelected(null) }}>
        <Modal.Backdrop variant="blur"><Modal.Container size="lg" scroll="inside"><Modal.Dialog>
          <Modal.Header><Modal.Heading>{t('maintenanceDetail')}</Modal.Heading><p className="text-sm text-muted">{selected?.account_name || selected?.account_id}</p></Modal.Header>
          <Modal.Body className="space-y-3">
            {selected ? <>
              <div className="flex flex-wrap items-center gap-2"><Chip size="sm" variant="soft" color={dailyStatusColor(selected.status)}>{t(`dailyStatus_${selected.status}`)}</Chip><span className="text-xs text-muted">{t(`maintenanceKind_${selected.kind}`)} · {t(`dailyTrigger_${selected.trigger}`)} · {formatTime(selected.created_at)}</span></div>
              <p className="break-words text-sm">{selected.summary || '—'}</p>
              <div className="divide-y divide-separator">{selected.steps?.map((step, index) => <div key={`${step.step}-${index}`} className="py-2"><div className="flex items-center justify-between gap-3 text-xs"><span>{dailyStepLabel(step.step, t)}</span><Chip size="sm" variant="soft" color={dailyStatusColor(step.status)}>{t(`dailyStatus_${step.status}`)}</Chip></div>{step.reused ? <p className="mt-1 text-xs text-muted">{t('maintenanceStepReused')}</p> : null}{step.message ? <p className="mt-1 break-words text-xs text-muted">{step.message}</p> : null}</div>)}</div>
              {selected.next_retry_at ? <p className="text-xs text-muted">{t('dailyTasksRetryAt')}: {formatTime(selected.next_retry_at)}</p> : null}
              <dl className="grid gap-2 text-xs text-muted sm:grid-cols-2"><div><dt>{t('maintenanceFinishedAt')}</dt><dd className="mono">{formatTime(selected.finished_at)}</dd></div><div><dt>{t('maintenanceAttempt')}</dt><dd className="mono">{selected.attempt}</dd></div></dl>
            </> : null}
          </Modal.Body><Modal.Footer><Button size="sm" variant="ghost" onPress={() => setSelected(null)}>{t('close')}</Button></Modal.Footer>
        </Modal.Dialog></Modal.Container></Modal.Backdrop>
      </Modal.Root>
    </div>
  )
}

function MaintenanceLogList({ items, loading, onSelect, formatTime }: { items: MaintenanceLog[]; loading: boolean; onSelect: (item: MaintenanceLog) => void; formatTime: (value?: string) => string }) {
  const { t } = useI18n()
  return <Card className="overflow-hidden p-0" aria-busy={loading}>
    {loading ? <LogsRequestListSkeleton /> : !items.length ? <EmptyPanel icon={<CalendarCheck size={22} />} title={t('maintenanceEmpty')} hint={t('maintenanceEmptyHint')} /> : (
      <Table><Table.ScrollContainer><Table.Content aria-label={t('maintenanceLogs')}>
        <Table.Header>
          <Table.Column isRowHeader>{t('logsColTime')}</Table.Column>
          <Table.Column>{t('logsColProvider')}</Table.Column>
          <Table.Column>{t('logsColAccount')}</Table.Column>
          <Table.Column>{t('maintenanceKind')}</Table.Column>
          <Table.Column>{t('maintenanceTrigger')}</Table.Column>
          <Table.Column>{t('logsColStatus')}</Table.Column>
          <Table.Column>{t('maintenanceSummary')}</Table.Column>
          <Table.Column>{t('maintenanceDetail')}</Table.Column>
        </Table.Header>
        <Table.Body>{items.map((item) => (
          <Table.Row key={item.id}>
            <Table.Cell><span className="mono whitespace-nowrap text-xs">{formatTime(item.created_at)}</span></Table.Cell>
            <Table.Cell><span className="whitespace-nowrap text-xs">{accountProviderLabel(item.provider, item.region, t)}</span></Table.Cell>
            <Table.Cell><span className="text-sm font-medium">{item.account_name || item.account_id}</span></Table.Cell>
            <Table.Cell><span className="whitespace-nowrap text-xs">{t(`maintenanceKind_${item.kind}`)}</span></Table.Cell>
            <Table.Cell><span className="whitespace-nowrap text-xs text-muted">{t(`dailyTrigger_${item.trigger}`)}</span></Table.Cell>
            <Table.Cell><Chip size="sm" variant="soft" color={dailyStatusColor(item.status)}>{t(`dailyStatus_${item.status}`)}</Chip></Table.Cell>
            <Table.Cell><p className="line-clamp-2 min-w-36 max-w-lg break-words text-xs text-muted">{item.summary || '—'}</p></Table.Cell>
            <Table.Cell><Button size="sm" variant="ghost" onPress={() => onSelect(item)}>{t('maintenanceDetail')}</Button></Table.Cell>
          </Table.Row>
        ))}</Table.Body>
      </Table.Content></Table.ScrollContainer></Table>
    )}
  </Card>
}
