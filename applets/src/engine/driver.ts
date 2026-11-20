// The engine: everything that is the same whichever program is in the terminal.
//
// A program's declaration (DriverSpec) says what its gates look like, how to tell working from idle,
// and which extra functions the control plane may call. How the cursor moves, how a paste is
// submitted and when input may be written belong to the engine, so a declaration cannot get them
// wrong.
//
//   defineDriver(spec)        runs the frame loop for one program and exposes `snapshot` and
//                             `say(text, id?)` to the control plane, plus spec.commands. It mirrors
//                             status (starting|busy|waiting|idle|dead), elapsed, tokens, question,
//                             choices and spec.vars. `say` queues the text; once it is pasted and
//                             submitted the engine calls the control plane's `sayResult({id, ok,
//                             error})` when an id was given; a repeated id is ignored.
//   send(text, mode)          queues input from a command, typed ('type') or pasted ('paste').
//   tail(screen, n)           the last n painted lines.
//   options helpers           chooseByLabel, chooseNearCursorByLabel, readOptions, parseOption,
//                             readCursorOptions, clean (see options.ts); keys (see keys.ts).
//
// Example:
//
//   defineDriver({ name: 'sh', version: 1,
//     observe: (s) => ({ kind: 'open', working: false, hasUI: /\$ $/.test(s) }),
//     commands: { clear: () => send('clear') } })

import { host, Owned } from './host'
import { ENTER, PASTE_END, PASTE_START } from './keys'
import {
  chooseByLabel,
  chooseNearCursorByLabel,
  Choice,
  clean,
  readCursorOptions,
  readOptions,
} from './options'

export {
  chooseByLabel,
  chooseNearCursorByLabel,
  clean,
  parseOption,
  readCursorOptions,
  readOptions,
} from './options'
export type { Choice } from './options'
export * from './keys'
export { host } from './host'

/** What the driver could work out from one frame. */
export interface Observation {
  /** 'dead' | 'waiting' | 'open' — 'open' means the program is up and running normally. */
  kind: 'dead' | 'waiting' | 'open'
  /** kind 'waiting': the dialog's question and options. */
  question?: string
  choices?: Choice[]
  /** kind 'open': is a turn actually in progress, and is the UI up at all? */
  working?: boolean
  hasUI?: boolean
}

export interface GateContext {
  screen: string
  tail: string
  frame: number
  /** Answer a dialog by label. Moves on one frame, confirms on a later one. */
  choose(want: RegExp): void
  write(data: string): boolean
}

export interface Gate {
  /** A name that shows up in logs; also documents what the gate is for. */
  name: string
  /** Matched against the WHOLE screen by default — several gates paint at the top of a tall pane. */
  when: RegExp | ((ctx: GateContext) => boolean)
  /** What to do while the gate is up. Return nothing; the engine re-runs it next frame. */
  act(ctx: GateContext): void
  /**
   * Act only every N frames (default 2). A TUI repainting itself needs time to digest keystrokes,
   * and answering twice in a row answers the NEXT dialog too.
   */
  every?: number
}

export interface DriverContext {
  screen: string
  tail: string
  frame: number
  status: string
  write(data: string): boolean
}

export interface DriverSpec {
  /** Reported to the control plane on startup, with the version below. */
  name: string
  version: number
  /** Gates are tried in order, before anything else, and a match ends the frame. */
  gates?: Gate[]
  /** Read one frame. Given the whole screen; most programs only trust the tail. */
  observe(screen: string): Observation
  /** Extra mirrored variables this program has (Claude has compaction and subagents; Codex none). */
  vars?: Record<string, unknown>
  /** Called every frame after status is decided, to update those extra variables. */
  report?(ctx: DriverContext, vars: Record<string, Owned<any>>, o: Observation): void
  /** Pull "6m45s" / "19.2k" out of a working screen. Cleared automatically when not working. */
  progress?(ctx: DriverContext): { elapsed?: string; tokens?: string } | null
  /** Called when the program is idle and no input is pending, e.g. to keep a mode selected. */
  onIdle?(ctx: DriverContext): void
  /** Last chance to change a message before it is pasted (Codex prepends its operator prompt). */
  beforeSay?(text: string): string
  /** Extra functions the control plane may call, on top of snapshot/say. */
  commands?: Record<string, (...args: any[]) => any>
  /** How many lines count as "the tail". Default 16. */
  tailLines?: number
}

/**
 * The last n painted lines.
 *
 * "Painted" is the point: a terminal is a fixed grid, so a program with a short conversation in a
 * tall pane leaves the bottom half blank — and a plain last-n-lines tail is then mostly emptiness
 * with the footer pushed out of view. A driver reading that tail concludes the program has not
 * started yet, forever, while a person looking at the same screen sees a working prompt. So trailing
 * blank lines are dropped first, and the tail is taken from where the content actually ends.
 */
export function tail(screen: string, n: number): string {
  const lines = screen.split('\n')
  let end = lines.length
  while (end > 0 && lines[end - 1].trim() === '') end--
  return lines.slice(Math.max(0, end - n), end).join('\n')
}

const tailOf = tail

/** How input reaches the program: a bracketed paste, or keys typed as they are. */
export type InputMode = 'paste' | 'type'

interface Pending {
  text: string
  mode: InputMode
  id?: string
}

// The input queue. Module state, because each screen runs its driver in a VM of its own.
const inputQueue: Pending[] = []

/**
 * Queue text for the program, submitted with Enter once the program is ready for it. For commands:
 * `send('/compact', 'type')`.
 */
export function send(text: string, mode: InputMode = 'type'): 'queued' {
  inputQueue.push({ text, mode })
  return 'queued'
}

export function defineDriver(spec: DriverSpec): void {
  const tailLines = spec.tailLines ?? 16
  const term = host.term

  // --- the variables every control plane expects ----------------------------
  const statusVar = host.own('status', 'starting') // starting|busy|waiting|idle|dead
  const elapsed = host.own('elapsed', '')
  const tokens = host.own('tokens', '')
  const question = host.own('question', '')
  const choices = host.own<Choice[]>('choices', [])
  const extra: Record<string, Owned<any>> = {}
  for (const key of Object.keys(spec.vars ?? {})) extra[key] = host.own(key, spec.vars![key])

  // --- input ----------------------------------------------------------------
  // One entry at a time: written while the applet holds the input lease, then Enter a couple of
  // frames later, because a TUI ingests a paste asynchronously and an Enter written with it is
  // swallowed into the paste. The lease is held from the first write to the Enter, so a person's
  // keys cannot land in between; while a person holds it, the entry waits. Nothing is written
  // before the program shows its UI or while a dialog is up.
  let inFlight: (Pending & { enterIn: number }) | null = null

  function report(p: Pending, ok: boolean, error?: string) {
    if (p.id !== undefined) host.call('sayResult', { id: p.id, ok, error: error ?? '' })
  }

  function pump(status: string) {
    if (inFlight) {
      if (!term.hold(true)) return
      if (--inFlight.enterIn > 0) return
      const done = inFlight
      inFlight = null
      const ok = term.write(ENTER)
      term.hold(false)
      report(done, ok, ok ? undefined : 'the Enter could not be written')
      return
    }
    if (!inputQueue.length || (status !== 'idle' && status !== 'busy')) return
    if (!term.hold(true)) return
    const next = inputQueue[0]
    const body = next.mode === 'paste' ? PASTE_START + next.text + PASTE_END : next.text
    if (!term.write(body)) {
      term.hold(false)
      return
    }
    inputQueue.shift()
    inFlight = { ...next, enterIn: 2 }
  }

  // --- the frame loop -------------------------------------------------------
  let frame = 0

  term.onChange(() => {
    frame++
    const screen = term.read()
    const tail = tailOf(screen, tailLines)
    const ctx: DriverContext = { screen, tail, frame, status: statusVar.get(), write: (d) => term.write(d) }

    // tmux's own marker for a program that exited is the pane's last painted line; matching it
    // anywhere else would take text the program printed for its death.
    const last = tail.split('\n').pop() ?? ''
    if (/^Pane is dead \(status \d+/.test(last.trim())) {
      statusVar.set('dead')
      return
    }

    // Gates first: while one is up the program is not going to do anything else.
    for (const gate of spec.gates ?? []) {
      const gctx: GateContext = {
        screen,
        tail,
        frame,
        write: (d) => term.write(d),
        choose: (want) => {
          // Numbered options first, then the unnumbered shape with a cursor.
          if (chooseByLabel((d) => term.write(d), screen, want) === 'absent') {
            chooseNearCursorByLabel((d) => term.write(d), screen, want)
          }
        },
      }
      const up = typeof gate.when === 'function' ? gate.when(gctx) : gate.when.test(screen)
      if (!up) continue
      if (frame % (gate.every ?? 2) === 0) gate.act(gctx)
      statusVar.set('starting')
      return
    }

    const o = spec.observe(screen)
    let st: string = o.kind
    if (o.kind === 'open') st = o.working ? 'busy' : o.hasUI ? 'idle' : 'starting'
    ctx.status = st

    pump(st)
    if (st === 'idle' && !inFlight && !inputQueue.length && spec.onIdle) spec.onIdle(ctx)

    statusVar.set(st)
    question.set(o.kind === 'waiting' ? o.question || '' : '')
    choices.set(o.kind === 'waiting' ? o.choices || [] : [])

    if (st === 'busy') {
      const p = spec.progress ? spec.progress(ctx) : null
      // Only overwrite on a match: a spinner scrolled out of view must not blank a live turn.
      if (p && p.elapsed !== undefined) elapsed.set(p.elapsed)
      if (p && p.tokens !== undefined) tokens.set(p.tokens)
    } else {
      elapsed.set('')
      tokens.set('')
    }

    if (spec.report) spec.report(ctx, extra, o)
  })

  // --- what the control plane may call --------------------------------------
  host.expose('snapshot', () => term.read())
  // A message with an id is taken once: the control plane may resend one it never heard back about,
  // and typing it twice would deliver it twice.
  const seen = new Set<string>()
  host.expose('say', (text: unknown, id?: unknown) => {
    if (id !== undefined) {
      if (seen.has(String(id))) return 'queued'
      seen.add(String(id))
    }
    const body = spec.beforeSay ? spec.beforeSay(String(text)) : String(text)
    inputQueue.push({ text: body, mode: 'paste', id: id === undefined ? undefined : String(id) })
    return 'queued'
  })
  for (const name of Object.keys(spec.commands ?? {})) host.expose(name, spec.commands![name])

  host.call('screenReady', { driver: spec.name, version: spec.version }).then((ack) => {
    host.log('server acked screenReady: ' + JSON.stringify(ack))
  })
}

/** Re-exported for declarations that need to read the option list themselves. */
export const engine = { readOptions, clean }
