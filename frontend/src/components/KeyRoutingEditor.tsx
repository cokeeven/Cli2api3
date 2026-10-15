import { Description, Label, NumberField } from '@heroui/react'
import type { KeyRoutingPolicy, PlatformRoutingRule } from '@/api/keys'
import { ProviderMark } from '@/components/ProviderMark'
import { CompactSwitch } from '@/components/ui/CompactSwitch'
import { FormRow } from '@/components/ui/FormRow'
import { accountProviderLabel } from '@/lib/provider'

type Translate = (key: string, vars?: Record<string, string | number>) => string
type PlatformOption = { provider: string; region: string; value: string }

export function KeyRoutingEditor({ options, value, onChange, t }: {
  options: PlatformOption[]
  value: KeyRoutingPolicy
  onChange: (value: KeyRoutingPolicy) => void
  t: Translate
}) {
  const custom = value.platforms.length > 0
  function changeRule(option: PlatformOption, field: 'priority' | 'max_inflight', next: number) {
    const existing = value.platforms.find((rule) => rule.provider === option.provider && rule.region === option.region)
    const rule: PlatformRoutingRule = { provider: option.provider, region: option.region, priority: 0, max_inflight: 0, ...existing, [field]: next }
    onChange({ ...value, platforms: [...value.platforms.filter((item) => item.provider !== option.provider || item.region !== option.region), rule] })
  }
  return (
    <section className="space-y-4 border-t border-separator pt-4">
      <div>
        <Label className="text-sm font-medium">{t('keyRoutingTitle')}</Label>
        <Description className="mt-1 text-xs leading-5 text-muted">{t('keyRoutingHint')}</Description>
      </div>
      <CompactSwitch
        isSelected={custom}
        ariaLabel={t('keyRoutingCustom')}
        label={t('keyRoutingCustom')}
        onChange={(selected) => onChange({ ...value, platforms: selected ? options.map(({ provider, region }) => ({ provider, region, priority: 0, max_inflight: 0 })) : [] })}
      />
      {custom ? <div className="space-y-4 rounded-lg bg-surface-secondary p-3">
        {options.map((option) => {
          const rule = value.platforms.find((item) => item.provider === option.provider && item.region === option.region)
          const name = accountProviderLabel(option.provider, option.region, t)
          return <div key={option.value} className="space-y-2">
            <div className="flex items-center gap-2 text-sm font-medium"><ProviderMark provider={option.provider} size={14} />{name}</div>
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-1">
                <Label className="text-xs text-muted">{t('keyPlatformPriority')}</Label>
                <NumberField value={rule?.priority ?? 0} minValue={0} maxValue={100000} onChange={(next) => changeRule(option, 'priority', next ?? 0)}>
                  <NumberField.Group><NumberField.DecrementButton /><NumberField.Input aria-label={`${name} ${t('keyPlatformPriority')}`} /><NumberField.IncrementButton /></NumberField.Group>
                </NumberField>
              </div>
              <div className="space-y-1">
                <Label className="text-xs text-muted">{t('keyPlatformCap')}</Label>
                <NumberField value={rule?.max_inflight || NaN} minValue={1} maxValue={10000} onChange={(next) => changeRule(option, 'max_inflight', Number.isFinite(next) ? next! : 0)}>
                  <NumberField.Group><NumberField.DecrementButton /><NumberField.Input placeholder={t('keyPlatformUnlimited')} aria-label={`${name} ${t('keyPlatformCap')}`} /><NumberField.IncrementButton /></NumberField.Group>
                </NumberField>
              </div>
            </div>
          </div>
        })}
        <p className="text-xs leading-5 text-muted">{t('keyPlatformRulesHint')}</p>
      </div> : <p className="text-xs leading-5 text-muted">{t('keyRoutingRandom')}</p>}
      <FormRow label={t('keyCrossFailover')} hint={t('keyCrossFailoverHint')}>
        <CompactSwitch isSelected={value.cross_provider_failover} ariaLabel={t('keyCrossFailover')} onChange={(selected) => onChange({ ...value, cross_provider_failover: selected })} />
      </FormRow>
    </section>
  )
}
