import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { createRequire } from 'node:module'
import test from 'node:test'

const frontendRoot = path.resolve(import.meta.dirname, '..')
const require = createRequire(path.join(frontendRoot, 'package.json'))
const ts = require('typescript')
const React = require('react')
const { renderToStaticMarkup } = require('react-dom/server')
const { MemoryRouter } = require('react-router-dom')
const sourcePath = path.join(frontendRoot, 'src/pages/ModelUsagePage.tsx')
const source = fs.readFileSync(sourcePath, 'utf8')
const sourceTree = ts.createSourceFile(sourcePath, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
const functionNames = new Set(['formatCount', 'formatDate', 'valueLabel', 'usageCoverage', 'budgetFor', 'ModelRow'])
const selected = sourceTree.statements
  .filter((node) => ts.isFunctionDeclaration(node) && functionNames.has(node.name?.text))
  .map((node) => node.getText(sourceTree))
assert.equal(selected.length, functionNames.size, 'must render the actual production ModelRow and its helpers')

const compiled = ts.transpileModule(
  "import { Link } from 'react-router-dom';\n" + selected.join('\n') + '\nexport { ModelRow };',
  { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX } },
).outputText
const componentModule = { exports: {} }
new Function('require', 'module', 'exports', compiled)(require, componentModule, componentModule.exports)
const ModelRow = componentModule.exports.ModelRow

function usageRow(provider = 'workbuddy') {
  return {
    provider,
    region: 'cn',
    account_id: 'synthetic-account',
    model: `${provider}/synthetic-model`,
    requests: 1,
    attempts: 1,
    succeeded_attempts: 1,
    failed_attempts: 0,
    canceled_attempts: 0,
    failover_attempts: 0,
    length_requests: 0,
    prompt_tokens: null,
    completion_tokens: null,
    known_usage_attempts: 0,
    estimated_usage_attempts: 0,
    missing_usage_attempts: 1,
    cache_read_tokens: null,
    cache_write_tokens: null,
    cache_known_requests: 0,
    cache_missing_requests: 1,
  }
}

function renderRows(rows, workbuddyBudgets) {
  const data = {
    data: { rows, recent: [], from: '2026-10-19T00:00:00Z', to: '2026-10-19T01:00:00Z' },
    accounts: [],
    routes: [],
    workbuddy_budgets: workbuddyBudgets,
  }
  const children = rows.map((row) => React.createElement(ModelRow, {
    key: `${row.provider}/${row.model}`,
    row,
    data,
    accountName: 'Synthetic account',
    lang: 'zh',
    t: (key) => key,
  }))
  return renderToStaticMarkup(React.createElement(
    MemoryRouter,
    null,
    React.createElement('table', null, React.createElement('tbody', null, children)),
  ))
}

test('Trae row renders with a legacy null WorkBuddy budget field and keeps budget unknown', () => {
  const row = usageRow('trae')
  const html = renderRows([row], null)
  assert.ok(html.includes(row.model))
  assert.ok(html.includes('modelUsageUnknown'))
  assert.ok(html.includes('— / —'), 'missing token usage must remain unknown, not become zero')
})

test('WorkBuddy row without a budget renders unknown rather than a fabricated estimate', () => {
  const row = usageRow('workbuddy')
  const html = renderRows([row], null)
  assert.ok(html.includes(row.model))
  assert.ok(html.includes('modelUsageUnknown'))
  assert.ok(html.includes('— / —'))
})

test('empty observations render an empty table body without attempting a budget lookup', () => {
  assert.equal(renderRows([], null), '<table><tbody></tbody></table>')
})

test('a real WorkBuddy budget renders its known remaining estimate', () => {
  const row = usageRow('workbuddy')
  const html = renderRows([row], [{
    account_id: row.account_id,
    region: row.region,
    model: row.model,
    estimated_remaining: 17,
  }])
  assert.ok(html.includes('17 modelUsageEstimatedShort'))
  assert.ok(html.includes('— / —'), 'unknown token usage must remain unknown when a budget estimate exists')
})
