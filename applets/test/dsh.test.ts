import { test } from 'node:test'
import assert from 'node:assert/strict'
import { makeHost, runDriver } from './fake-host'

function loadDriver() {
  const host = makeHost()
  runDriver('dsh')
  return host
}

// Frames as `microteams acp-screen -- dsh --profile acp` paints them (taken from a real run against
// dsh 0.2.1-rc.2 in tmux, 120 columns). The last painted line is acp-screen's status line.

const READY = [
  'agent: deepseek-harness-acp 0.0.1 (ACP v1)',
  'new session 6c5a43e1-4b5e-4e70-918c-d2e7b026883a',
  '── ○ ready · deepseek-v4-flash ──',
].join('\n')

const WORKING = [
  'agent: deepseek-harness-acp 0.0.1 (ACP v1)',
  'new session 6c5a43e1-4b5e-4e70-918c-d2e7b026883a',
  '› run the tests',
  '⚙ Run go test ./...',
  '── ● working 1m12s · deepseek-v4-flash ──',
].join('\n')

const EXITED = [
  'acp-screen: agent exited: status 1',
  '── ✕ exited (agent exited: status 1) · deepseek-v4-flash ──',
].join('\n')

test('the ready status line is idle', () => {
  const host = loadDriver()
  host.frame(READY)
  assert.equal(host.vars['status'], 'idle')
})

test('the working status line is busy, and its elapsed time is reported', () => {
  const host = loadDriver()
  host.frame(WORKING)
  assert.equal(host.vars['status'], 'busy')
  assert.equal(host.vars['elapsed'], '1m12s')
})

test('a finished turn is idle again, with the elapsed time cleared', () => {
  const host = loadDriver()
  host.frame(WORKING)
  host.frame(READY.replace('○ ready', '○ ready'))
  assert.equal(host.vars['status'], 'idle')
  assert.equal(host.vars['elapsed'], '')
})

test('a short turn shows seconds', () => {
  const host = loadDriver()
  host.frame(WORKING.replace('1m12s', '7s'))
  assert.equal(host.vars['elapsed'], '7s')
})

test('the agent exiting, or the pane dying, is dead', () => {
  const host = loadDriver()
  host.frame(EXITED)
  assert.equal(host.vars['status'], 'dead')
  const host2 = loadDriver()
  host2.frame('Pane is dead (status 1)')
  assert.equal(host2.vars['status'], 'dead')
})

test('the words "ready" or "working" in the transcript are not the status', () => {
  const host = loadDriver()
  host.frame('› is the build ready?\nThe build is working now.\n')
  assert.equal(host.vars['status'], 'starting')
})

test('a person typing after the status line does not hide it', () => {
  const host = loadDriver()
  host.frame(READY.replace('──\n', '── › half a sentence\n').replace(/──$/, '── › half a sentence'))
  assert.equal(host.vars['status'], 'idle')
})

test('interrupt sends Ctrl-C, which acp-screen turns into session/cancel', () => {
  const host = loadDriver()
  host.frame(WORKING)
  host.clearKeys()
  host.exposed['interrupt']()
  assert.equal(host.keys(), '\x03')
})

test('the applet announces itself as the dsh driver', () => {
  const host = loadDriver()
  const ready = host.calls.find((c) => c.name === 'screenReady')
  assert.ok(ready)
  assert.equal((ready.args as Record<string, unknown>)['driver'], 'dsh')
})
