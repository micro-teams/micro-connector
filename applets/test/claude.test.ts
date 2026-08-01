// The Claude declaration, driven frame by frame against a fake terminal.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { makeHost, runDriver } from './fake-host'
import { ENTER } from '../src/engine/keys'

const PASTE_START = '\x1b[200~'

function loadDriver() {
  const host = makeHost()
  runDriver('claude')
  return host
}

const IDLE = ['', '╭──────────────╮', '│ >            │', '╰──────────────╯', '? for shortcuts', ''].join('\n')

const BYPASS_GATE = [
  'WARNING: Claude Code running in Bypass Permissions mode',
  '',
  '❯ 1. No, exit',
  '  2. Yes, I accept',
  '',
  'Enter to confirm',
].join('\n')

const WORKING = ['', 'Thinking… (6m 45s · ↓ 19.2k tokens)', 'esc to interrupt', ''].join('\n')

test('the bypass gate is answered by label, and never with a bare Enter', () => {
  const host = loadDriver()
  host.frame(BYPASS_GATE)
  host.frame(BYPASS_GATE) // the gate acts every other frame: the TUI needs time to digest
  const typed = host.keys()
  assert.ok(!typed.includes(ENTER), 'a bare Enter here answers "No, exit" and kills the agent')
  assert.ok(typed.includes('\x1b[B'), 'it should step down to "Yes, I accept"')
})

test('the folder-trust gate is answered with Enter', () => {
  const host = loadDriver()
  host.frame('Is this a project you created or one you trust?\n\n1. Yes, I trust this folder')
  assert.ok(host.keys().includes(ENTER))
})

test('an idle prompt reads as idle, a working one as busy, with its elapsed and tokens', () => {
  const host = loadDriver()
  host.frame(IDLE)
  assert.equal(host.vars.status, 'idle')
  host.frame(WORKING)
  assert.equal(host.vars.status, 'busy')
  assert.equal(host.vars.elapsed, '6m45s')
  assert.equal(host.vars.tokens, '19.2k')
  host.frame(IDLE)
  assert.equal(host.vars.elapsed, '', 'a finished turn must not leave a stale timer up')
})

test('a message is pasted atomically and submitted on a later frame, never the same one', () => {
  const host = loadDriver()
  host.frame(IDLE)
  host.clearKeys()
  host.exposed.say('hello there')
  const afterSay = host.keys()
  assert.ok(afterSay.startsWith(PASTE_START), 'the body goes in as one bracketed paste')
  assert.ok(
    !afterSay.endsWith(ENTER),
    'an Enter written now is swallowed into the paste — the message would sit in the box, unsent',
  )
  host.frame(IDLE)
  host.frame(IDLE)
  assert.ok(host.keys().endsWith(ENTER), 'and is submitted a couple of frames later')
})

test('nothing is typed while a human holds the keyboard; it is flushed when they let go', () => {
  const host = loadDriver()
  host.frame(IDLE)
  host.setWatched('viewerLevel', 'full')
  host.clearKeys()
  assert.equal(host.exposed.say('while you type'), 'buffered')
  assert.equal(host.keys(), '', 'not one keystroke may land in a human’s input')
  host.setWatched('viewerLevel', 'passive')
  assert.ok(host.keys().includes('while you type'))
})

test('a dead pane is reported dead', () => {
  const host = loadDriver()
  host.frame('Pane is dead (status 1)')
  assert.equal(host.vars.status, 'dead')
})

test('the driver announces itself so the control plane knows what it is talking to', () => {
  const host = loadDriver()
  assert.equal(host.calls[0].name, 'screenReady')
  assert.equal((host.calls[0].args as any).driver, 'claude')
})
