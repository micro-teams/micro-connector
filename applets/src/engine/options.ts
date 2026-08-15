// Reading and answering the option dialogs these TUIs put in the way.
//
// The single most expensive lesson in this repository lives here: THE SELECTION WRAPS. An earlier
// driver moved to an option by pressing UP nine times ("go firmly to the top") and then stepping
// down. On a two-option dialog nine UPs land on option 2 — an odd number of wraps — and the
// following DOWN wraps back to option 1. Aiming at "Yes, I accept" arrived at "No, exit" every
// single time, and the Enter quit Claude Code three seconds after launch. On a brand-new machine
// the agent died before it could read anything.
//
// So this module offers no way to move to an absolute position. It only moves RELATIVE to where the
// cursor actually is, which cannot drift no matter how the list wraps.

import { DOWN, ENTER, UP } from './keys'

export interface Choice {
  n: number
  label: string
}

/** Box-drawing characters the TUIs paint around dialogs; never part of a label. */
export const clean = (line: string): string => line.replace(/[│╭╮╰╯]/g, '')

/** `❯ 2. Yes, I accept` -> {n: 2, label: 'Yes, I accept'}; anything else -> null. */
export function parseOption(line: string): Choice | null {
  const m = clean(line).match(/^\s*[❯>]?\s*(\d+)\.\s+(.*\S)\s*$/)
  return m ? { n: parseInt(m[1], 10), label: m[2].trim() } : null
}

export interface ParsedOption {
  opt: Choice
  selected: boolean
}

export function readOptions(screen: string): ParsedOption[] {
  const out: ParsedOption[] = []
  for (const line of screen.split('\n')) {
    const opt = parseOption(line)
    if (opt) out.push({ opt, selected: /❯/.test(line) })
  }
  return out
}

/**
 * The matcher for "this exact option, the one I already found on this screen".
 *
 * A caller that has read the options and picked one holds a label, not a pattern — and turning a
 * label into a bare `new RegExp(escaped)` quietly asks a different question: not "which option is
 * this" but "which option CONTAINS this". `chooseByLabel` takes the first match, so whenever one
 * label is a prefix of another the answer is the wrong option, and it is the earlier one — the
 * position the cursor usually starts from, so the mistake also costs the fewest keystrokes and is
 * the easiest to make. Claude offers exactly that shape: "Yes" alongside "Yes, and don't ask
 * again".
 *
 * Anchoring both ends makes it the identity question it was always meant to be. Escaping is done
 * here too, so no caller has to remember that a label like "1.5x (recommended)" is also a regex.
 */
export const exactly = (label: string): RegExp =>
  new RegExp(`^${label.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`)

/** What one call to `chooseByLabel` did, so a caller can tell "not yet" from "no such option". */
export type ChooseResult = 'confirmed' | 'moved' | 'not-ready' | 'absent'

/**
 * Answer the dialog by the option's LABEL, never by its number: the numbering and the recommended
 * default belong to the program, and two gates in a row having opposite option orders is exactly
 * the trap this exists to survive.
 *
 * The move and the Enter are deliberately NOT done together. The TUI is still digesting the cursor
 * keys when a same-burst Enter arrives, so the Enter either vanishes or lands on the option we were
 * moving away from. One call moves; a later call — on a later frame, seeing the cursor where it now
 * is — confirms. Every step is idempotent and none of them can answer the dialog wrongly alone.
 */
export function chooseByLabel(
  write: (data: string) => void,
  screen: string,
  want: RegExp,
): ChooseResult {
  const options = readOptions(screen)
  const target = options.findIndex((o) => want.test(o.opt.label))
  if (target < 0) return 'absent'
  const current = options.findIndex((o) => o.selected)
  if (current === target) {
    write(ENTER)
    return 'confirmed'
  }
  // No cursor on screen: a frame caught mid-repaint. Wait for one that shows it rather than guess.
  if (current < 0) return 'not-ready'
  const step = target > current ? DOWN : UP
  for (let i = 0; i < Math.abs(target - current); i++) write(step)
  return 'moved'
}
