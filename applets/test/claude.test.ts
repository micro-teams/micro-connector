// The Claude declaration, driven frame by frame against a fake terminal.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { makeHost, runDriver } from './fake-host'
import { DOWN, ENTER } from '../src/engine/keys'

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

// A short conversation in a tall pane leaves the bottom half of the grid blank. Read naively, the
// last sixteen lines are then almost all empty and the footer is nowhere in them — so the driver
// decides the program is still starting, forever, while a person looking at the same screen sees a
// working prompt. Found by asserting on the driver's opinion rather than on the pane, which is the
// whole reason the end-to-end test asks the driver what it thinks.
test('a prompt near the top of a tall pane is still a prompt', () => {
  const host = loadDriver()
  host.frame(IDLE + '\n'.repeat(20))
  assert.equal(host.vars.status, 'idle')
})

// --- login mode ------------------------------------------------------------
// A control plane that must log a machine IN sets the watched var `mode` to 'login'. Everything
// below must stay dormant otherwise, so a normal agent screen is untouched.

const THEME = [
  'Choose the text style that looks best with your terminal:',
  '',
  '❯ 1. Dark mode',
  '  2. Light mode',
  '',
  'Enter to confirm',
].join('\n')

const METHOD = [
  'Select login method:',
  '',
  '❯ 1. Anthropic Console (API usage billing)',
  '  2. Claude account with subscription',
  '',
  'Enter to confirm · Esc to cancel',
].join('\n')

const OAUTH = [
  'Browser didn’t open? Use the url below to sign in:',
  '',
  'https://claude.ai/oauth/authorize?code=true&client_id=abc&response_type=code&state=deadbeef',
  '',
  'Paste code here if prompted >',
].join('\n')

function login(): ReturnType<typeof loadDriver> {
  const host = loadDriver()
  host.setWatched('mode', 'login')
  return host
}

test('login mode: the theme picker is confirmed with the default', () => {
  const host = login()
  host.frame(THEME)
  host.frame(THEME) // gates act every other frame
  assert.ok(host.keys().includes(ENTER), 'a default theme is fine; confirm it')
})

test('login mode: the login-method dialog lands on subscription, never a bare Enter onto Console', () => {
  const host = login()
  host.clearKeys()
  host.frame(METHOD)
  host.frame(METHOD)
  const typed = host.keys()
  assert.ok(typed.includes(DOWN), 'it must step down to the subscription option, not confirm Console')
  assert.ok(
    !typed.startsWith(ENTER),
    'a first-move Enter would accept "Anthropic Console" and bill per-token',
  )
})

test('login mode: an already-onboarded idle prompt kicks off /login exactly once', () => {
  const host = login()
  host.frame(IDLE)
  assert.ok(host.keys().includes('/login'), 'idle + not logged in => run /login')
  host.clearKeys()
  host.frame(IDLE)
  assert.equal(host.keys(), '', 'and never again once issued')
})

test('normal mode: an idle prompt never types /login (login logic is dormant)', () => {
  const host = loadDriver() // no mode set
  host.frame(IDLE)
  assert.ok(!host.keys().includes('/login'), 'a normal agent must not be driven into a login')
})

test('login mode: the OAuth URL is mirrored up and the state becomes awaitingCode', () => {
  const host = login()
  host.frame(OAUTH)
  assert.equal(
    host.vars.oauthUrl,
    'https://claude.ai/oauth/authorize?code=true&client_id=abc&response_type=code&state=deadbeef',
  )
  assert.equal(host.vars.loginState, 'awaitingCode')
})

test('login mode: a captured URL survives later frames that no longer show it', () => {
  const host = login()
  host.frame(OAUTH)
  host.frame(IDLE)
  assert.ok((host.vars.oauthUrl as string).includes('oauth/authorize'), 'the URL must not blank out')
})

test('login mode: the success screen sets loginState to success', () => {
  const host = login()
  host.frame(OAUTH)
  host.frame('Login successful. Press Enter to continue…')
  assert.equal(host.vars.loginState, 'success')
})

test('normal mode: the OAuth variables stay empty', () => {
  const host = loadDriver()
  host.frame(OAUTH) // same screen, but no login mode
  assert.equal(host.vars.oauthUrl, '')
  assert.equal(host.vars.loginState, '')
})
