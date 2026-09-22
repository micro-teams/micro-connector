// Pi, as a declaration.
//
// Everything here is a statement about what pi paints — verified against real 0.87.x screens.
//
// pi is the simplest program we drive, and its smallness is load-bearing: unlike Claude Code it
// has NO gates at all. Tools run without a consent dialog, a fresh session opens straight to the
// prompt with no trust screen and no first-run wizard (model and provider are the operator's
// config file, not an onboarding flow), and there is nothing to keep selected between turns. What
// remains is the shared skeleton — the working bar, the dead pane, the hasUI line — which is why
// this file is short. If a future pi paints a dialog, that is a gate to add here, not an engine
// change.

import { tail as tailOf, defineDriver, Observation } from '../engine/driver'

defineDriver({
  name: 'pi',
  version: 1,

  observe: (screen): Observation => {
    const tail = tailOf(screen, 16).split('\n')
    const tailStr = tail.join('\n')

    if (/Pane is dead \(status/.test(tailStr)) return { kind: 'dead' }

    // A working turn paints a bar: "── ⠋ Working ─────…" — an animated braille spinner
    // (U+2801–U+28FF) glued to the word "Working". The bar is up for the WHOLE turn — the model
    // thinking and every tool it runs — and is gone when the turn ends, so a finished turn leaves
    // nothing behind to masquerade as busy. (The claude declaration had to exclude static glyphs
    // for exactly this reason; pi has no such trap.) Both signals are required, so the word
    // "Working" in ordinary output and a stray braille in scrollback cannot each, alone, flip
    // the verdict.
    const working = /[⠁-⣿]/.test(tailStr) && /\bWorking\b/.test(tailStr)

    // The context line "1.4%/100k (auto)   mock-1" — session token usage over the window, and the
    // active model — is painted for as long as the TUI is up, so it is the hasUI signal. The
    // percentage is cumulative session usage, NOT a per-turn count: that is why progress() below
    // reports nothing rather than mislabeling it.
    const hasUI =
      /\d+(\.\d+)?%\/\d+[kKmM]?\s*\(/.test(tailStr) || tail.filter((l) => l.trim()).length > 3
    return { kind: 'open', working, hasUI }
  },

  // pi paints no per-turn elapsed or token count — the context line above is cumulative, and
  // pulling it out as "this turn's tokens" would report a number that goes backwards. Honest null,
  // as the codex declaration does for what its footer does not carry.
  progress: () => null,
})
