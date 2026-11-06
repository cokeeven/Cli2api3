import { useState } from 'react'
import { Button, Card, Input, Label, ListBox, Select } from '@heroui/react'
import { updateSystemSettings, type ModelAlternativePolicy, type ModelAlternativeRule, type SystemSettings } from '@/api/system'
import { CompactSwitch } from '@/components/ui/CompactSwitch'
import { PageAlert } from '@/components/ui/PageAlert'
import { useI18n } from '@/hooks/useI18n'

export function ModelAlternativeSettings({ settings, onSaved }: { settings: SystemSettings | null; onSaved: (settings: SystemSettings) => void }) {
  const { t } = useI18n()
  const [draft, setDraft] = useState<ModelAlternativePolicy | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const value = draft ?? settings?.model_alternatives ?? { enabled: false, rules: [] }
  function changeRule(index: number, patch: Partial<ModelAlternativeRule>) {
    setDraft({ ...value, rules: value.rules.map((rule, i) => i === index ? { ...rule, ...patch } : rule) })
  }
  async function save() {
    if (!draft || !settings) return
    setBusy(true); setError('')
    try { onSaved(await updateSystemSettings({ model_alternatives: draft })); setDraft(null) }
    catch (err) { setError(err instanceof Error ? err.message : String(err)) }
    finally { setBusy(false) }
  }
  return <Card data-gsap-reveal>
    <Card.Header><Card.Title>{t('modelAlternativesTitle')}</Card.Title><Card.Description>{t('modelAlternativesHint')}</Card.Description></Card.Header>
    <Card.Content className="space-y-3">
      {error ? <PageAlert title={error} /> : null}
      <div className="flex items-center justify-between gap-3"><Label>{t('modelAlternativesEnabled')}</Label><CompactSwitch ariaLabel={t('modelAlternativesEnabled')} isSelected={value.enabled} isDisabled={!settings || busy} onChange={(enabled) => setDraft({ ...value, enabled })} /></div>
      {value.rules.map((rule, i) => <div key={i} className="space-y-2 rounded-lg border border-separator p-3">
        <div className="grid grid-cols-2 gap-2">
          <div className="space-y-1"><Label className="text-xs text-muted">{t('modelAlternativesProvider')}</Label><Select fullWidth aria-label={t('modelAlternativesProvider')} value={rule.provider} isDisabled={busy} onChange={(value) => { if (typeof value === 'string' && value) changeRule(i, { provider: value, region: 'cn' }) }}><Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger><Select.Popover><ListBox><ListBox.Item id="workbuddy" textValue="WorkBuddy"><Label>WorkBuddy</Label><ListBox.ItemIndicator /></ListBox.Item><ListBox.Item id="trae" textValue="Trae"><Label>Trae</Label><ListBox.ItemIndicator /></ListBox.Item></ListBox></Select.Popover></Select></div>
          <div className="space-y-1"><Label className="text-xs text-muted">{t('modelAlternativesRegion')}</Label><Select fullWidth aria-label={t('modelAlternativesRegion')} value={rule.region} isDisabled={busy} onChange={(value) => { if (typeof value === 'string' && value) changeRule(i, { region: value }) }}><Select.Trigger><Select.Value /><Select.Indicator /></Select.Trigger><Select.Popover><ListBox><ListBox.Item id="cn" textValue="CN"><Label>CN</Label><ListBox.ItemIndicator /></ListBox.Item>{rule.provider === 'workbuddy' ? <ListBox.Item id="global" textValue="Global"><Label>Global</Label><ListBox.ItemIndicator /></ListBox.Item> : null}</ListBox></Select.Popover></Select></div>
        </div>
        <label className="block text-xs text-muted">{t('modelAlternativesFrom')}<Input value={rule.requested_model} disabled={busy} onChange={(e) => changeRule(i, { requested_model: e.target.value })} /></label>
        <label className="block text-xs text-muted">{t('modelAlternativesTo')}<Input value={rule.alternative_model} disabled={busy} onChange={(e) => changeRule(i, { alternative_model: e.target.value })} /></label>
        <Button size="sm" variant="secondary" isDisabled={busy} onPress={() => setDraft({ ...value, rules: value.rules.filter((_, j) => i !== j) })}>{t('modelAlternativesRemove')}</Button>
      </div>)}
      <p className="text-xs text-muted">{t('modelAlternativesLimit')}</p>
      <div className="flex justify-between gap-2"><Button size="sm" variant="secondary" isDisabled={!settings || busy || value.rules.length >= 32} onPress={() => setDraft({ ...value, rules: [...value.rules, { provider: 'workbuddy', region: 'cn', requested_model: '', alternative_model: '' }] })}>{t('modelAlternativesAdd')}</Button><Button size="sm" isDisabled={!settings || busy || !draft} isPending={busy} onPress={() => void save()}>{t('save')}</Button></div>
    </Card.Content>
  </Card>
}
