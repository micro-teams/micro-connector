// Builds one self-contained JS file per program declaration.
//
// The output is what a control plane serves to a connector, so it must run inside goja: no modules,
// no imports, no runtime dependencies — one IIFE per driver. `target: es2016` keeps the output close
// to the source, which matters when the thing you are debugging is a terminal on someone else's
// machine and the only artifact you have is this file.
import { build } from 'esbuild'
import { readdirSync } from 'node:fs'

const drivers = readdirSync('src/drivers').filter((f) => f.endsWith('.ts'))

await build({
  entryPoints: drivers.map((f) => `src/drivers/${f}`),
  outdir: 'dist',
  bundle: true,
  format: 'iife',
  target: 'es2016',
  platform: 'neutral',
  logLevel: 'info',
})
console.log(`built ${drivers.length} driver(s): ${drivers.join(', ')}`)
