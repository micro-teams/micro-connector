// The host API a screen applet is given: the one global `connector` the runtime installs (see
// cli/runtime/api.go), typed for the engine and the drivers.
//
//   host.term.read()          the visible screen
//   host.term.write(data)     types data; false when refused (a person holds the input lease)
//   host.term.hold(on)        takes or releases the input lease for the applet; false when refused
//   host.term.onChange(fn)    every screen change, and every ~1.2 s
//   host.own / watch          variables the control plane mirrors / owns
//   host.expose / call        functions the control plane calls / answers
//   host.log(message)
//
// Against a protocol-1 runtime (no lease) hold always succeeds.
//
// Example: if (host.term.hold(true)) { host.term.write('hi') }

/** A script-owned variable the host mirrors up to the control plane. */
export interface Owned<T> {
  get(): T
  set(value: T): void
}

/** A control-plane-owned variable the script observes. */
export interface Watched<T> {
  get(): T
  onChange(fn: (value: T) => void): void
}

export interface Terminal {
  /** The current visible screen. */
  read(): string
  /** Keystrokes and control sequences; false when refused or failed. */
  write(data: string): boolean
  /** Take (true) or release (false) the input lease; false while a person holds it. */
  hold(on: boolean): boolean
  /** Every screen change and a ~1.2 s heartbeat, so a dialog nobody touches is still answered. */
  onChange(fn: () => void): void
}

export interface Host {
  term: Terminal
  own<T>(name: string, initial: T): Owned<T>
  watch<T>(name: string): Watched<T>
  expose(name: string, fn: (...args: any[]) => any): void
  call(name: string, args?: unknown): { then(fn: (result: any) => void): void }
  log(message: string): void
}

declare const globalThis: Record<string, any>

// A connector that predates the input lease (protocol 1) has no term.hold and returns nothing from
// term.write. Its drivers have no lease to respect there, so hold always succeeds and a write
// counts as done unless the runtime says false.
const raw = (globalThis as any).connector
export const host: Host = {
  ...raw,
  term: {
    read: () => raw.term.read(),
    onChange: (fn: () => void) => raw.term.onChange(fn),
    write: (data: string) => raw.term.write(data) !== false,
    hold: (on: boolean) => (raw.term.hold ? raw.term.hold(on) : true),
  },
}
