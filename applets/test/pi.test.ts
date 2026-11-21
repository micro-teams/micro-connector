import { test } from 'node:test'
import assert from 'node:assert/strict'
import { makeHost, runDriver } from './fake-host'

function loadDriver() {
  const host = makeHost()
  runDriver('pi')
  return host
}

// Frames taken from a real pi 0.87.0 screen (mock model behind), with the conversation kept short
// so the footer is inside the tail the driver trusts.

const IDLE = [
  ' run the tool pi-mark-1',
  ' done',
  '────────────────────────────────────────────────────────────────────────────',
  '/tmp/piprobe/work',
  '0.0%/100k (auto)                        mock-1',
].join('\n')

const WORKING = [
  ' run the tool pi-mark-1',
  '── ⠋ Working ────────────────────────────────────────────────────────────────',
  '────────────────────────────────────────────────────────────────────────────',
  '/tmp/piprobe/work',
  '1.4%/100k (auto)                        mock-1',
].join('\n')

// The same bar with a different spinner glyph mid-animation.
const WORKING_MID = WORKING.replace('⠋', '⣻')

test('an idle pi screen is idle, not still starting', () => {
  const host = loadDriver()
  host.frame(IDLE)
  assert.equal(host.vars['status'], 'idle')
})

test('the working bar reads busy, and the spinner glyph is not the verdict', () => {
  const host = loadDriver()
  host.frame(WORKING)
  assert.equal(host.vars['status'], 'busy')
  host.frame(WORKING_MID)
  assert.equal(host.vars['status'], 'busy')
})

test('a finished turn is idle again: pi leaves no spinner behind', () => {
  const host = loadDriver()
  host.frame(WORKING)
  host.frame(IDLE)
  assert.equal(host.vars['status'], 'idle')
})

test('the word Working alone, or braille alone, is not a turn', () => {
  const host = loadDriver()
  host.frame(' the user asked about a Working file\n done\n' + IDLE.slice(IDLE.indexOf('──')))
  assert.equal(host.vars['status'], 'idle')
  // A lone braille spacer with no Working bar.
  host.frame(' done\n⠁\n' + IDLE.slice(IDLE.indexOf('──')))
  assert.equal(host.vars['status'], 'idle')
})

test('a dead pane is dead', () => {
  const host = loadDriver()
  host.frame('Pane is dead (status 127)')
  assert.equal(host.vars['status'], 'dead')
})

test('the applet announces itself as the pi driver', () => {
  const host = loadDriver()
  const ready = host.calls.find((c) => c.name === 'screenReady')
  assert.ok(ready, 'screenReady must be called at startup')
  assert.equal((ready.args as Record<string, unknown>)['driver'], 'pi')
})

test('an empty screen is starting, not idle', () => {
  const host = loadDriver()
  host.frame('')
  assert.equal(host.vars['status'], 'starting')
})
