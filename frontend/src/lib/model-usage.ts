export type BudgetIdentity = {
  account_id: string
  region: string
  model: string
  plan?: string
  request_path?: string
}

// Keep the React row identity aligned with the dimensions used by the server
// when it groups WorkBuddy budget observations. A JSON tuple avoids delimiter
// collisions when any dimension contains punctuation.
export function workbuddyBudgetKey(item: BudgetIdentity): string {
  return JSON.stringify([
    item.account_id,
    item.region,
    item.model,
    item.plan ?? '',
    item.request_path ?? '',
  ])
}

export function budgetProgressPercent(used: number | null | undefined, reference: number | null | undefined): number | null {
  if (used == null || reference == null || !Number.isFinite(used) || !Number.isFinite(reference) || used < 0 || reference <= 0) return null
  return Math.max(0, Math.min(100, Math.round((used / reference) * 100)))
}

export function budgetProgressExceedsReference(used: number | null | undefined, reference: number | null | undefined): boolean {
  return used != null && reference != null && Number.isFinite(used) && Number.isFinite(reference) && reference > 0 && used > reference
}
