import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { createRequire } from 'node:module'
import test from 'node:test'

const frontendRoot = path.resolve(import.meta.dirname, '..')
const require = createRequire(path.join(frontendRoot, 'package.json'))
const ts = require('typescript')
const sourcePath = path.join(frontendRoot, 'src/pages/ModelUsagePage.tsx')
const source = fs.readFileSync(sourcePath, 'utf8')
const sourceTree = ts.createSourceFile(sourcePath, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
const functionNames = new Set(['formatCount', 'usageCoverage'])
const selected = sourceTree.statements
  .filter((node) => ts.isFunctionDeclaration(node) && functionNames.has(node.name?.text))
  .map((node) => node.getText(sourceTree))
assert.equal(selected.length, functionNames.size, 'must exercise the compact page production helpers')

const compiled = ts.transpileModule(
  selected.join('\n') + '\nexport { formatCount, usageCoverage };',
  { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } },
).outputText
const componentModule = { exports: {} }
new Function('require', 'module', 'exports', compiled)(require, componentModule, componentModule.exports)
const { formatCount, usageCoverage } = componentModule.exports

test('compact model usage keeps missing counts unknown while retaining explicit zero', () => {
  assert.equal(formatCount(null), '—')
  assert.equal(formatCount(undefined), '—')
  assert.equal(formatCount(Number.NaN), '—')
  assert.equal(formatCount(0), '0')
  assert.equal(formatCount(12345), new Intl.NumberFormat().format(12345))
})

test('compact model usage coverage reports known, estimated and missing attempt counts', () => {
  assert.equal(usageCoverage({ known_usage_attempts: 2, estimated_usage_attempts: 1, missing_usage_attempts: 3 }), '2 / 6')
  assert.equal(usageCoverage({ known_usage_attempts: 0, estimated_usage_attempts: 0, missing_usage_attempts: 2 }), '0 / 2')
})
