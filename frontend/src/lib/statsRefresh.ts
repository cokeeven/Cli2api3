export type VisibilityDocument = Pick<Document, 'hidden' | 'addEventListener' | 'removeEventListener'>
export type IntervalHandle = ReturnType<typeof setInterval>

/** Start a periodic callback only while the document is visible. */
export function startVisibleInterval(
  doc: VisibilityDocument,
  callback: () => void,
  intervalMs = 30_000,
  schedule: typeof setInterval = setInterval,
  cancel: typeof clearInterval = clearInterval,
) {
  let timer: IntervalHandle | null = null
  const syncVisibility = () => {
    if (doc.hidden) {
      if (timer !== null) cancel(timer)
      timer = null
      return
    }
    if (timer === null) timer = schedule(callback, intervalMs)
  }

  doc.addEventListener('visibilitychange', syncVisibility)
  syncVisibility()
  return () => {
    doc.removeEventListener('visibilitychange', syncVisibility)
    if (timer !== null) cancel(timer)
    timer = null
  }
}
