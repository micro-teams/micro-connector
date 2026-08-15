// The regression tests for the most expensive bug this repository knows about.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { chooseByLabel, exactly, parseOption, readOptions } from '../src/engine/options'
import { DOWN, ENTER, UP } from '../src/engine/keys'

const gate = (cursorOn: number) =>
  [
    '  Bypass Permissions mode',
    '',
    `${cursorOn === 1 ? '❯' : ' '} 1. No, exit`,
    `${cursorOn === 2 ? '❯' : ' '} 2. Yes, I accept`,
  ].join('\n')

test('an option line is parsed, box drawing and cursor and all', () => {
  assert.deepEqual(parseOption('❯ 2. Yes, I accept'), { n: 2, label: 'Yes, I accept' })
  assert.deepEqual(parseOption('│  1. No, exit  │'), { n: 1, label: 'No, exit' })
  assert.equal(parseOption('not an option'), null)
  assert.equal(readOptions(gate(1)).length, 2)
})

// The bug: the old driver pressed UP nine times to "go firmly to the top" and then stepped down.
// The list WRAPS, so nine UPs on a two-option gate land on option 2 and the following DOWN wraps
// back to option 1 — aiming at "Yes, I accept" pressed "No, exit", which quit Claude Code three
// seconds after launch. A brand-new machine's first agent killed itself before reading anything.
test('choosing moves relative to the cursor, so a wrapping list cannot mislead it', () => {
  const w: string[] = []
  // Cursor on the destructive option, exactly as the gate opens.
  assert.equal(chooseByLabel((d) => w.push(d), gate(1), /yes,?\s*i\s*accept/i), 'moved')
  assert.deepEqual(w, [DOWN], 'one step down: from option 1 to option 2, nothing more')

  // Next frame, with the cursor where the move put it: now, and only now, confirm.
  const w2: string[] = []
  assert.equal(chooseByLabel((d) => w2.push(d), gate(2), /yes,?\s*i\s*accept/i), 'confirmed')
  assert.deepEqual(w2, [ENTER])
})

test('moving upward is just as relative', () => {
  const w: string[] = []
  chooseByLabel((d) => w.push(d), gate(2), /no, exit/i)
  assert.deepEqual(w, [UP])
})

// Two failures that must never be answered by guessing: a frame caught mid-repaint (no cursor
// anywhere) and a dialog that simply does not offer what we want.
test('an unreadable or absent option is reported, never guessed at', () => {
  const noCursor = gate(0)
  const w: string[] = []
  assert.equal(chooseByLabel((d) => w.push(d), noCursor, /yes/i), 'not-ready')
  assert.equal(w.length, 0, 'nothing may be typed into a frame we cannot read')
  assert.equal(chooseByLabel((d) => w.push(d), gate(1), /no such option/i), 'absent')
  assert.equal(w.length, 0)
})

// T-070. A caller that has already FOUND its option holds a label, not a pattern — and the old
// code turned that label into a bare `new RegExp(escaped)`, which asks "which option contains
// this" rather than "which option is this". chooseByLabel takes the first match, so when one label
// is a prefix of another the answer is the earlier option. Claude ships exactly that shape.
const prefixGate = (cursorOn: number) =>
  [
    `${cursorOn === 1 ? '❯' : ' '} 1. Yes, and don't ask again`,
    `${cursorOn === 2 ? '❯' : ' '} 2. Yes`,
    `${cursorOn === 3 ? '❯' : ' '} 3. No`,
  ].join('\n')

test('an exact label matches its own option, not a longer one that contains it', () => {
  const w: string[] = []
  // Wanting option 2 ("Yes") with the cursor already on it: there is nothing to do but confirm.
  // Unanchored, /Yes/ finds "Yes, and don't ask again" first and steps AWAY to option 1.
  assert.equal(chooseByLabel((d) => w.push(d), prefixGate(2), exactly('Yes')), 'confirmed')
  assert.deepEqual(w, [ENTER], 'it was already on the right option; moving is the bug')
})

test('an exact label still escapes what would otherwise be regex', () => {
  const screen = ['❯ 1. Use 1.5x (recommended)', '  2. Use 15x'].join('\n')
  const w: string[] = []
  // Unescaped, `1.5x (recommended)` is a pattern whose parens are a group and whose dot is a
  // wildcard — it would match nothing here, and the option would read as absent.
  assert.equal(chooseByLabel((d) => w.push(d), screen, exactly('Use 1.5x (recommended)')), 'confirmed')
})
