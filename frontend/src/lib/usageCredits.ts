export type CreditsDisplay =
  | { kind: 'unknown' }
  | { kind: 'amount'; amount: number }
  | { kind: 'partial'; amount: number; unknownCount: number }
  | { kind: 'stale'; amount: number }

type GatewayCreditsDay = {
  credits_actual?: number
  credits_coverage?: string
  credits_known_attempts?: number
  credits_unknown_attempts?: number
}

type OfficialCredits = {
  status?: string
  credits?: number | null
  unknown_records?: number
}

function validCredits(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0
}

function validCount(value: unknown): value is number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0
}

export function gatewayConfirmedCredits(day: GatewayCreditsDay): CreditsDisplay {
  const known = day.credits_known_attempts
  const unknown = day.credits_unknown_attempts
  if (!validCredits(day.credits_actual) || !validCount(known) || !validCount(unknown)) return { kind: 'unknown' }
  if (day.credits_coverage === 'complete' && known > 0 && unknown === 0) return { kind: 'amount', amount: day.credits_actual }
  if (day.credits_coverage === 'partial' && known > 0 && unknown > 0) return { kind: 'partial', amount: day.credits_actual, unknownCount: unknown }
  return { kind: 'unknown' }
}

export function officialAccountCredits(summary?: OfficialCredits | null): CreditsDisplay {
  if (!summary || !validCredits(summary.credits)) return { kind: 'unknown' }
  if (summary.status === 'complete') return { kind: 'amount', amount: summary.credits }
  if (summary.status === 'partial' && validCount(summary.unknown_records) && summary.unknown_records > 0) {
    return { kind: 'partial', amount: summary.credits, unknownCount: summary.unknown_records }
  }
  if (summary.status === 'stale') return { kind: 'stale', amount: summary.credits }
  return { kind: 'unknown' }
}
