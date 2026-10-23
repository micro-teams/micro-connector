// DeepSeek Harness, as a declaration.
//
// dsh has no terminal UI of its own, so the screen does not show dsh at all: it shows
// `microteams acp-screen`, our thin front end that speaks ACP to `dsh --profile acp` and paints a
// transcript. That makes this the easiest declaration there is: we wrote the screen, and its last
// painted line is a status line made to be read:
//
//   ── ● working 1m12s · deepseek-v4-flash ──
//   ── ○ ready · deepseek-v4-flash ──
//   ── ✕ exited (status 1) ──
//
// There are no gates: acp-screen answers permission requests itself, and there is no trust screen
// or first-run wizard. A chat message is the engine's ordinary bracketed paste plus Enter, which
// acp-screen turns into one ACP prompt.

import { tail as tailOf, defineDriver, host, Observation } from '../engine/driver'

// The status line acp-screen keeps at the bottom; anything after it is what a person is typing.
const STATUS = /──\s+(● working(?:\s+(\d+m\d{2}s|\d+s))?|○ ready|✕ exited|… starting)/

function statusLine(screen: string): RegExpExecArray | null {
  const lines = tailOf(screen, 8).split('\n')
  for (let i = lines.length - 1; i >= 0; i--) {
    const m = STATUS.exec(lines[i])
    if (m) return m
  }
  return null
}

defineDriver({
  name: 'dsh',
  version: 1,

  observe: (screen): Observation => {
    const tail = tailOf(screen, 8)
    if (/Pane is dead \(status/.test(tail)) return { kind: 'dead' }
    const m = statusLine(screen)
    if (!m) return { kind: 'open', working: false, hasUI: false }
    if (m[1].indexOf('✕ exited') === 0) return { kind: 'dead' }
    if (m[1].indexOf('● working') === 0) return { kind: 'open', working: true, hasUI: true }
    if (m[1].indexOf('○ ready') === 0) return { kind: 'open', working: false, hasUI: true }
    return { kind: 'open', working: false, hasUI: false }
  },

  // The elapsed time is on the status line; acp-screen shows no per-turn token count.
  progress: (ctx) => {
    const m = statusLine(ctx.screen)
    return m && m[2] ? { elapsed: m[2] } : null
  },

  commands: {
    // Cancel the turn in flight: acp-screen maps Ctrl-C to ACP session/cancel.
    interrupt: () => {
      host.term.write('\x03')
      return true
    },
  },
})
