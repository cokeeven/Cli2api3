import { getLocalTimeZone, parseAbsoluteToLocal, toCalendarDateTime, type DateValue } from '@internationalized/date'

export type DateRangeValue = { start: DateValue; end: DateValue }

/** DateValue (local wall time) to ISO instant; the end rounds through its selected minute. */
export function dateValueToISO(value: DateValue | null | undefined, endOfMinute = false) {
  if (!value) return undefined
  const date = value.toDate(getLocalTimeZone())
  if (!Number.isFinite(date.getTime())) return undefined
  if (endOfMinute) date.setSeconds(59, 999)
  return date.toISOString()
}

/** ISO instant to local CalendarDateTime for the picker; null when unparsable. */
export function isoToDateValue(iso: string | null | undefined): DateValue | null {
  if (!iso) return null
  const ms = Date.parse(iso)
  if (!Number.isFinite(ms)) return null
  try {
    return toCalendarDateTime(parseAbsoluteToLocal(new Date(ms).toISOString()))
  } catch {
    return null
  }
}
