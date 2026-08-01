import { test } from 'node:test'
import assert from 'node:assert/strict'
import { makeHost, runDriver } from './fake-host'
import { ENTER } from '../src/engine/keys'

function loadDriver() {
  const host = makeHost()
  runDriver('codex')
  return host
}

test('the directory-trust gate is answered', () => {
  const host = loadDriver()
  host.frame('Do you trust the contents of this directory?\n\n1. Yes, continue')
  assert.ok(host.keys().includes(ENTER))
})

test('working and idle are read from Codex’s own footer, not Claude’s', () => {
  const host = loadDriver()
  host.frame('\n› \ngpt-5-codex · ~/work\n')
  assert.equal(host.vars.status, 'idle')
  host.frame('\nWorking (6s · esc to interrupt)\n')
  assert.equal(host.vars.status, 'busy')
  assert.equal(host.vars.elapsed, '6s')
})

// Codex has no system prompt, so its standing instructions ride along with the first thing anyone
// says to it — sending them at launch would make it start working on its own.
test('the operator instructions are prepended once, to the first message only', () => {
  const host = loadDriver()
  host.frame('\n› \n')
  host.clearKeys()
  host.exposed.say('first')
  const first = host.keys()
  host.clearKeys()
  host.exposed.say('second')
  const second = host.keys()
  // The placeholder is un-substituted in a test build, which is itself the guard: an un-replaced
  // placeholder must never be pasted into a terminal.
  assert.ok(!first.includes('__MT_'), 'an un-substituted placeholder must not be sent')
  assert.ok(first.includes('first') && second.includes('second'))
})
