import fs from 'node:fs'
import path from 'node:path'
import { pathToFileURL } from 'node:url'
import { createRequire } from 'node:module'

const root = path.resolve(process.argv[2])
const worker = path.join(root, 'components', 'worker')
const cliRoot = path.join(root, 'components', 'cli')
const { inspectQodercliSource, PINNED_QODERCLI_VERSION } = await import(pathToFileURL(path.join(worker, 'src', 'compat.mjs')).href)
if (process.version !== 'v22.23.3') throw new Error('Runtime Node version disagrees with the package pin')
for (const [scope, name] of [['@qoder-ai', 'qodercli'], ['@qodercn-ai', 'qoderclicn']]) {
  const dir = path.join(cliRoot, 'node_modules', scope, name)
  const meta = JSON.parse(fs.readFileSync(path.join(dir, 'package.json'), 'utf8'))
  if (meta.version !== PINNED_QODERCLI_VERSION) throw new Error(`Wrong ${name} compatibility version`)
  const report = inspectQodercliSource(fs.readFileSync(path.join(dir, 'bundle', `${name}.js`), 'utf8'), { version: meta.version })
  if (!report.ok) throw new Error(report.message)
}
const undici = JSON.parse(fs.readFileSync(path.join(worker, 'node_modules', 'undici', 'package.json'), 'utf8'))
if (undici.version !== '6.29.1') throw new Error('Worker dependency version mismatch')
// Load the actual Windows native addon without starting either CLI or reading a HOME.
const require = createRequire(pathToFileURL(path.join(cliRoot, 'package.json')))
const sharp = require('sharp')
if (sharp.versions.sharp !== '0.35.5') throw new Error('Windows native addon version mismatch')
await sharp({ create: { width: 1, height: 1, channels: 4, background: '#ffffff' } }).png().toBuffer()
if (!sharp.versions?.vips) throw new Error('Windows image runtime addon is unavailable')
console.log(JSON.stringify({ node: process.version, qodercli: PINNED_QODERCLI_VERSION, qoderclicn: PINNED_QODERCLI_VERSION, undici: undici.version, sharp: sharp.versions.sharp, windowsAddonLoaded: true }))
