// Request history stores lifecycle states, not a live request registry.
// These values mean no terminal result was recorded for the log row.
export function hasUnfinishedRequestLogStatus(status?: string | null) {
  const normalized = status?.trim().toLowerCase()
  return normalized === 'started' || normalized === 'streaming'
}
