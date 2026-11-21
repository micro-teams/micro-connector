// A host to test drivers against: a terminal whose screen the test sets, and a record of every
// keystroke written to it.
//
// This is what makes it possible to test a driver at all. The real host is a tmux session on
// another machine; here it is an object, and a "frame" is a function call.
import { readFileSync } from 'node:fs'
import type { Host } from '../src/engine/host'

export interface FakeHost extends Host {
  screen: string
  written: string[]
  vars: Record<string, unknown>
  watched: Record<string, unknown>
  exposed: Record<string, (...args: any[]) => any>
  calls: Array<{ name: string; args: unknown }>
  /** Deliver one frame to the driver, optionally setting the screen first. */
  frame(screen?: string): void
  /** Everything written since the last clear, joined. */
  keys(): string
  clearKeys(): void
  setWatched(name: string, value: unknown): void
}

export function makeHost(): FakeHost {
  const listeners: Array<() => void> = []
  const watchListeners: Record<string, Array<(v: unknown) => void>> = {}
  const h: any = {
    screen: '',
    written: [],
    vars: {},
    watched: { viewerLevel: 'passive', label: '' },
    exposed: {},
    calls: [],
    term: {
      read: () => h.screen,
      write: (d: string) => h.written.push(d),
      onChange: (fn: () => void) => listeners.push(fn),
    },
    own: (name: string, initial: unknown) => {
      h.vars[name] = initial
      return { get: () => h.vars[name], set: (v: unknown) => (h.vars[name] = v) }
    },
    watch: (name: string) => ({
      get: () => h.watched[name],
      onChange: (fn: (v: unknown) => void) => (watchListeners[name] ||= []).push(fn),
    }),
    expose: (name: string, fn: (...a: any[]) => any) => (h.exposed[name] = fn),
    call: (name: string, args: unknown) => {
      h.calls.push({ name, args })
      return { then: () => undefined }
    },
    log: () => undefined,
    frame: (screen?: string) => {
      if (screen !== undefined) h.screen = screen
      for (const fn of listeners) fn()
    },
    keys: () => h.written.join(''),
    clearKeys: () => (h.written = []),
    setWatched: (name: string, value: unknown) => {
      h.watched[name] = value
      for (const fn of watchListeners[name] ?? []) fn(value)
    },
  }
  ;(globalThis as any).connector = h
  return h as FakeHost
}

/**
 * Load a built driver into a fresh scope, the way the host does.
 *
 * Production runs the bundled IIFE in a brand-new goja VM for every screen — so module state starts
 * clean each time and top-level declarations cannot collide. Evaluating the same built file here
 * reproduces that exactly, and has the useful side effect of testing the artifact that actually
 * ships rather than the TypeScript it came from.
 */
export function runDriver(name: string): void {
  const code = readFileSync(new URL(`../dist/${name}.js`, import.meta.url), 'utf8')
  new Function(code)()
}
