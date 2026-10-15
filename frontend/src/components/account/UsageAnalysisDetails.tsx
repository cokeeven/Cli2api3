import { Chip, Table } from '@heroui/react'
import type { AccountUsage } from '@/api/usage'
import { formatQuotaAmount } from '@/lib/account'

type Translate = (key: string, vars?: Record<string, string | number>) => string

function reasonLabel(reason: string, t: Translate) {
  const keys: Record<string, string> = {
    daily_tokens: 'usageBlockDailyTokens', model_daily_tokens: 'usageBlockModelTokens', daily_credits: 'usageBlockDailyCredits',
    reserve_credits: 'usageBlockReserveCredits', credit_upper_bound_unknown: 'usageBlockUnknownCost', credit_balance_unknown: 'usageBlockUnknownBalance',
    credit_balance_stale: 'usageBlockStaleBalance', credit_balance_pending: 'usageBlockPendingBalance', credit_balance_unbounded: 'usageBlockPendingBalance',
    reserve_credits_unknown: 'usageBlockPendingBalance', account_quota_exhausted: 'usageBlockAccountQuota',
    daily_tokens_unknown: 'usageBlockUnknownTokens', model_daily_tokens_unknown: 'usageBlockUnknownModelTokens', daily_credits_unknown: 'usageBlockUnknownDailyCredits',
  }
  return keys[reason] ? t(keys[reason]) : reason
}

function dateLabel(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString()
}

export function UsageAnalysisDetails({ usage, t }: { usage: AccountUsage; t: Translate }) {
  return <>
    {usage.guard_states?.length ? <section className="space-y-2">
      <h3 className="text-sm font-medium">{t('usageBlockTitle')}</h3>
      <div className="divide-y divide-separator rounded-lg bg-surface-secondary px-3">
        {usage.guard_states.map((state, index) => <div key={`${state.reason}-${state.model || ''}-${index}`} className="space-y-1.5 py-2.5">
          <div className="flex flex-wrap items-center gap-2"><Chip size="sm" variant="soft" color="warning">{reasonLabel(state.reason, t)}</Chip><span className="text-xs text-muted">{t(state.scope === 'all_models' ? 'usageBlockAllModels' : state.scope === 'paid_models' ? 'usageBlockPaidModels' : state.scope === 'model' ? 'usageBlockOneModel' : 'usageUnknown')}</span>{state.model ? <span className="mono break-all text-xs">{state.model}</span> : null}</div>
          <p className="text-[11px] leading-5 text-muted">{state.reset_at ? t('usageBlockReset', { time: dateLabel(state.reset_at) }) : t('usageBlockRecoveryUnknown')}</p>
        </div>)}
      </div>
    </section> : null}
    {usage.quota_history?.length ? <section className="space-y-2">
      <h3 className="text-sm font-medium">{t('usageBalanceHistory')}</h3>
      <p className="text-xs leading-5 text-muted">{t('usageBalanceHistoryHint')}</p>
      <Table><Table.ScrollContainer className="max-h-52"><Table.Content aria-label={t('usageBalanceHistory')}>
        <Table.Header><Table.Column isRowHeader>{t('logsColTime')}</Table.Column><Table.Column>{t('usageAvailable')}</Table.Column><Table.Column>{t('usageExpiryTitle')}</Table.Column></Table.Header>
        <Table.Body>{usage.quota_history.map((point, index) => <Table.Row key={`${point.fetched_at}-${index}`}>
          <Table.Cell><span className="mono text-[11px]">{dateLabel(point.fetched_at)}</span></Table.Cell>
          <Table.Cell><span className="mono text-xs">{point.known ? `${formatQuotaAmount(point.available)} ${point.unit}` : t('usageUnknown')}</span></Table.Cell>
          <Table.Cell><div className="space-y-1 text-[11px] text-muted"><div>{t('usageExpiry7d', { amount: point.known ? `${formatQuotaAmount(point.expiring_7d)} ${point.unit}` : '—' })}</div><div>{t('usageExpiry30d', { amount: point.known ? `${formatQuotaAmount(point.expiring_30d)} ${point.unit}` : '—' })}</div></div></Table.Cell>
        </Table.Row>)}</Table.Body>
      </Table.Content></Table.ScrollContainer></Table>
    </section> : null}
  </>
}
