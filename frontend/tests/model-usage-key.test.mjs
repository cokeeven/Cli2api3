import assert from 'node:assert/strict'
import test from 'node:test'
import { budgetProgressExceedsReference, budgetProgressPercent, workbuddyBudgetKey } from '../src/lib/model-usage.ts'

test('WorkBuddy budget identity includes the full observation grouping dimensions', () => {
  const base = {
    account_id: 'acct-synthetic',
    region: 'cn',
    model: 'glm-5.3',
    plan: 'pro',
  }

  const chat = { ...base, request_path: 'openai_chat_completions' }
  const responses = { ...base, request_path: 'openai_responses' }

  assert.notEqual(workbuddyBudgetKey(chat), workbuddyBudgetKey(responses))
  assert.equal(workbuddyBudgetKey(chat), workbuddyBudgetKey({ ...chat }))
  assert.notEqual(
    workbuddyBudgetKey({ ...base, plan: 'pro/a', request_path: 'b' }),
    workbuddyBudgetKey({ ...base, plan: 'pro', request_path: 'a/b' }),
  )
})

test('budget progress keeps unknowns unavailable and clamps only known ratios', () => {
  assert.equal(budgetProgressPercent(null, 100), null)
  assert.equal(budgetProgressPercent(0, null), null)
  assert.equal(budgetProgressPercent(-1, 100), null)
  assert.equal(budgetProgressPercent(Number.NaN, 100), null)
  assert.equal(budgetProgressPercent(Number.NEGATIVE_INFINITY, 100), null)
  assert.equal(budgetProgressPercent(0, Number.NaN), null)
  assert.equal(budgetProgressPercent(0, Number.POSITIVE_INFINITY), null)
  assert.equal(budgetProgressPercent(0, 100), 0)
  assert.equal(budgetProgressPercent(25, 100), 25)
  assert.equal(budgetProgressPercent(130, 100), 100)
  assert.equal(budgetProgressPercent(10, 0), null)
  assert.equal(budgetProgressExceedsReference(150, 100), true)
  assert.equal(budgetProgressExceedsReference(100, 100), false)
  assert.equal(budgetProgressExceedsReference(150, null), false)
  assert.equal(budgetProgressExceedsReference(-1, 100), false)
  assert.equal(budgetProgressExceedsReference(Number.POSITIVE_INFINITY, 100), false)
})
