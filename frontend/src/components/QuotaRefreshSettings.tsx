import { useState } from 'react'
import { Button, Card, NumberField } from '@heroui/react'
import { updateSystemSettings, type SystemSettings } from '@/api/system'
import { FormRow } from '@/components/ui/FormRow'
import { PageAlert } from '@/components/ui/PageAlert'
import { useI18n } from '@/hooks/useI18n'

export function QuotaRefreshSettings({ settings, onSaved }: {
  settings: SystemSettings | null
  onSaved: (settings: SystemSettings) => void
}) {
  const { t } = useI18n()
  const [draft, setDraft] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const value = draft ?? settings?.quota_refresh_interval_minutes ?? 30

  async function save() {
    if (!settings || draft == null || draft === settings.quota_refresh_interval_minutes) return
    setBusy(true)
    setError('')
    try {
      onSaved(await updateSystemSettings({ quota_refresh_interval_minutes: draft }))
      setDraft(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card data-gsap-reveal>
      <Card.Header>
        <Card.Title>{t('quotaRefreshIntervalTitle')}</Card.Title>
        <Card.Description>{t('quotaRefreshIntervalHint')}</Card.Description>
      </Card.Header>
      <Card.Content className="space-y-3">
        {error ? <PageAlert title={error} /> : null}
        <FormRow label={t('quotaRefreshIntervalLabel')} hint={t('quotaRefreshIntervalRange')}>
          <NumberField
            value={value}
            minValue={5}
            maxValue={1440}
            isDisabled={busy || !settings}
            onChange={(next) => setDraft(next != null && Number.isFinite(next) ? next : null)}
          >
            <NumberField.Group>
              <NumberField.DecrementButton />
              <NumberField.Input aria-label={t('quotaRefreshIntervalLabel')} />
              <NumberField.IncrementButton />
            </NumberField.Group>
          </NumberField>
        </FormRow>
        <div className="flex justify-end">
          <Button size="sm" variant="secondary" isDisabled={!settings || busy || draft == null || draft === settings.quota_refresh_interval_minutes} isPending={busy} onPress={() => void save()}>
            {t('save')}
          </Button>
        </div>
      </Card.Content>
    </Card>
  )
}
