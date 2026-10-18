import assert from 'node:assert/strict'
import test from 'node:test'
import { workbuddyBudgetKey } from '../src/lib/model-usage.ts'

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
