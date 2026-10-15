import { useEffect, useState } from 'react'
import { Button, Chip, Modal } from '@heroui/react'
import { ArrowClockwise } from '@phosphor-icons/react'
import { fetchDailyTaskRuns, type DailyTaskRun } from '@/api/dailyTasks'
import { PageAlert } from '@/components/ui/PageAlert'
import { SkeletonBlock } from '@/components/ui/PageSkeletons'
import { EmptyPanel } from '@/components/ui/EmptyPanel'
import type { AccountRow } from '@/lib/account'
import { dailyStatusColor, dailyStepLabel } from '@/lib/dailyTasks'

type Props = { account: AccountRow; t: (key: string) => string; onClose: () => void }
export function AccountDailyTasksModal({ account, t, onClose }: Props) {
  const [runs, setRuns] = useState<DailyTaskRun[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  useEffect(() => {
    let current = true
    void fetchDailyTaskRuns(account.id).then((res) => { if (current) setRuns(res.data) })
      .catch((err: unknown) => { if (current) setError(err instanceof Error ? err.message : String(err)) })
      .finally(() => { if (current) setLoading(false) })
    return () => { current = false }
  }, [account.id])
  async function reload() {
    setLoading(true); setError('')
    try { setRuns((await fetchDailyTaskRuns(account.id)).data) }
    catch (err) { setError(err instanceof Error ? err.message : String(err)) }
    finally { setLoading(false) }
  }
  return (
    <Modal.Root isOpen onOpenChange={(open: boolean) => { if (!open) onClose() }}>
      <Modal.Backdrop variant="blur"><Modal.Container size="lg" scroll="inside"><Modal.Dialog>
        <Modal.Header><Modal.Heading>{t('dailyTasksHistory')}</Modal.Heading><p className="text-sm text-muted">{account.name || account.id}</p></Modal.Header>
        <Modal.Body className="space-y-3">
          {error ? <PageAlert title={error} /> : null}
          {loading ? <SkeletonBlock className="h-40 w-full" /> : runs.length ? runs.map((run) => (
            <section key={run.id} className="rounded-lg border border-separator p-3">
              <div className="flex flex-wrap items-center gap-2">
                <Chip size="sm" variant="soft" color={dailyStatusColor(run.status)}>{t(`dailyStatus_${run.status}`)}</Chip>
                <span className="text-xs text-muted">{t(`dailyTrigger_${run.trigger}`)} · {new Date(run.started_at).toLocaleString()}</span>
              </div>
              <div className="mt-2 divide-y divide-separator">{(run.report.steps || []).map((step, index) => (
                <div key={`${step.step}-${index}`} className="py-2">
                  <div className="flex items-center justify-between gap-3 text-xs"><span>{dailyStepLabel(step.step, t)}</span><Chip size="sm" variant="soft" color={dailyStatusColor(step.status)}>{t(`dailyStatus_${step.status}`)}</Chip></div>
                  {step.message ? <p className="mt-1 break-words text-xs text-muted">{step.message}</p> : null}
                </div>
              ))}</div>
              {run.next_retry_at ? <p className="mt-2 text-xs text-muted">{t('dailyTasksRetryAt')}: {new Date(run.next_retry_at).toLocaleString()}</p> : null}
            </section>
          )) : <EmptyPanel title={t('dailyTasksEmpty')} hint={t('dailyTasksHint')} />}
        </Modal.Body>
        <Modal.Footer><Button size="sm" variant="ghost" onPress={onClose}>{t('close')}</Button><Button size="sm" variant="secondary" isPending={loading} onPress={() => void reload()}><ArrowClockwise size={14} />{t('refresh')}</Button></Modal.Footer>
      </Modal.Dialog></Modal.Container></Modal.Backdrop>
    </Modal.Root>
  )
}
