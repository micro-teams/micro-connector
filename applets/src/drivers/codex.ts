// Codex, as a declaration. It is separate from Claude Code's because what differs between the two
// (gates, working screens, option lists) is exactly what a shared declaration would get wrong.
//
// The control plane may set the variable `operatorPrompt`: standing instructions prepended once,
// to the first message. Codex has no system prompt, and sending them at launch would start a turn
// on its own. Commands: usage (/status).

import { tail as tailOf, defineDriver, ENTER, host, Observation, send } from '../engine/driver'

const operatorPrompt = host.watch<string>('operatorPrompt')
let sentOperatorPrompt = false

defineDriver({
  name: 'codex',
  version: 2,

  gates: [
    {
      // "Do you trust the contents of this directory?" on a fresh cwd. The default is "Yes,
      // continue" with "Press enter to continue", so a bare Enter answers it.
      name: 'directory trust',
      when: /Do you trust the contents/i,
      every: 1,
      act: (c) => c.write(ENTER),
    },
  ],

  observe: (screen): Observation => {
    const tail = tailOf(screen, 16).split('\n')
    const tailStr = tail.join('\n')
    // Codex paints "esc to interrupt" only while a turn is running — the same kind of signal as
    // Claude's, on a different line.
    const working = /esc to interrupt/i.test(tailStr)
    // The input box (›) or the "<model> … · <cwd>" footer means the UI is up, so: idle rather than
    // still starting. The line count is the same fallback the Claude declaration uses.
    const hasUI =
      /(^|\n)\s*›/.test(tailStr) || /·\s+\S/.test(tailStr) || tail.filter((l) => l.trim()).length > 3
    return { kind: 'open', working, hasUI }
  },

  // "Working (6s · esc to interrupt)". Codex's footer carries no token count.
  progress: (ctx) => {
    const m = ctx.tail.match(/Working\s*\(([^)]*)\)/i)
    if (!m) return null
    const tm = m[1].match(/(?:\d+h\s*)?(?:\d+m\s*)?\d+s|\d+m\b/)
    return tm ? { elapsed: tm[0].replace(/\s+/g, '') } : null
  },

  beforeSay: (text) => {
    const prompt = operatorPrompt.get()
    if (sentOperatorPrompt || !prompt) return text
    sentOperatorPrompt = true
    return prompt + '\n\n' + text
  },

  // Codex paints its session and usage limits for /status.
  commands: { usage: () => send('/status') },
})
