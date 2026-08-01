// Bundles the TypeScript tests to plain ESM so `node --test` can run them without a loader.
// The drivers themselves are bundled the same way for production, so the tests exercise the same
// pipeline rather than a parallel one.
import { build } from 'esbuild'
import { readdirSync } from 'node:fs'

const tests = readdirSync('test').filter((f) => f.endsWith('.test.ts'))
await build({
  entryPoints: tests.map((f) => `test/${f}`),
  outdir: 'dist-test',
  bundle: true,
  format: 'esm',
  target: 'node20',
  platform: 'node',
  external: ['node:*'],
  logLevel: 'warning',
})
