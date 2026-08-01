# micro-connector

A machine-side connector that lets a control plane run **terminal screens** and **applets** on
machines it cannot SSH into. The machine dials out; nothing dials in.

It is the shared core of three products that each grew their own copy — MicroTeams, CCProxy and
cheese — pulled into one place so the expensive part is maintained once.

## What is actually expensive here

Not the Go. `runtime` + `terminal` + `config` + `update` are about two thousand lines and anyone
could write them again. The expensive part is **understanding what a coding agent's terminal is
showing you**, and the tests that prove you still understand it after the next release. Everything
in that category was learned the hard way, on real machines:

| What happened | What it taught |
|---|---|
| On a brand-new machine Claude Code adds a bypass-permissions gate whose default is `No, exit` | Gates that never appear on an old machine still kill new ones |
| Aiming at "Yes, I accept" reliably pressed "No, exit" | **The option list wraps.** Absolute cursor positioning cannot work; only relative movement can |
| A same-frame Enter landed on the option we were leaving | Move and confirm must be split across frames |
| A pasted message was never submitted; the pane looked perfect | The Enter after a paste has to be deferred, and only a reply proves it landed |
| A 25KB message silently vanished | `tmux send-keys` refuses long commands; writes must be chunked, never splitting a rune |
| A screen whose tmux had died still read as alive | The applet runtime outlives the session it drives — something must confirm and report |

None of that is derivable from documentation. It is why this repository exists, and why its CI
drives a **real Claude Code** against a mock model rather than a fake program against a fake server.

## Layout

```
applets/    the screen engine + per-program declarations (claude, codex), built to JS
cli/        the Go library: terminal, runtime, session lifecycle, primitives  (in progress)
testbed/    a small reference control plane — the executable half of the contract  (planned)
.github/    the tests: real tmux, real Claude Code, mock model, matrixed by version
```

## Status

Early. The screen engine has been extracted from MicroTeams' two production drivers first, because
that is where the value is concentrated: `applets/src/engine` is the shared machinery and
`applets/src/drivers/*.ts` are one declaration per program. The Go library, the reference control
plane and the end-to-end tests follow, in that order.

The protocol between a connector and a control plane will be specified here, in this repository,
once it is extracted rather than merely implied — a specification that lives somewhere else is not
one this repository's readers can hold it to.

## License

MIT.
