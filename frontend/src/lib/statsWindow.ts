export type StatsWindow = 'today' | '1' | '24' | '168' | '720' | 'custom'
export type StatsWindowRange = { from?: string; to?: string } | null
export type StatsQuery = { hours: number } | { from: string; to: string }

export const STATS_WINDOW_LABEL: Record<StatsWindow, string> = {
  today: 'statsWindowToday',
  '1': 'statsWindow1h',
  '24': 'statsWindow24h',
  '168': 'statsWindow7d',
  '720': 'statsWindow30d',
  custom: 'statsWindowCustom',
}

/** Build a query for a local calendar day, fixed hour window, or custom range. */
export function statsQuery(window: StatsWindow, custom: StatsWindowRange, now = new Date()): StatsQuery | null {
  if (!Number.isFinite(now.getTime())) return null
  if (window === 'today') {
    const from = new Date(now)
    from.setHours(0, 0, 0, 0)
    return { from: from.toISOString(), to: now.toISOString() }
  }
  if (window === 'custom') {
    if (!custom?.from || !custom.to) return null
    const from = Date.parse(custom.from)
    const to = Date.parse(custom.to)
    if (!Number.isFinite(from) || !Number.isFinite(to) || from >= to) return null
    return { from: new Date(from).toISOString(), to: new Date(to).toISOString() }
  }
  if (window === '720') {
    const to = now.getTime()
    return { from: new Date(to - 30 * 24 * 60 * 60 * 1000).toISOString(), to: new Date(to).toISOString() }
  }
  return { hours: Number(window) }
}
