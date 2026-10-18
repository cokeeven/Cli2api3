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
