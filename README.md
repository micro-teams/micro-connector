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
cli/        the Go library: terminal, runtime, screens, enrolment, credentials, self-update
testbed/    a reference control plane, a connector made only of the library, and their e2e
docs/       the contract between a control plane and a connector
.github/    the tests: real tmux, real Claude Code, mock model, matrixed by version
```

Implementing a control plane? [`docs/protocol.md`](docs/protocol.md) is the specification, and
[`testbed/server.py`](testbed/server.py) is the same thing you can run.

## Using it

Two artifacts, versioned together, because a screen driver and the host that runs it are only ever
tested as a pair.

**The Go library** — build a connector for your own product:

```bash
go get github.com/micro-teams/micro-connector/cli@v0.1.0
```

```go
brand.Current = brand.Brand{Name: "yourthing", EnvPrefix: "YOURTHING", /* … */}

tm, _ := terminal.NewManager()
conn := ws.New(controlURL, token, apiBase)      // or httppoll, for a one-shot command
mgr := screen.NewManager(ctx, conn, tm)
_ = conn.Run(ctx, mgr.Dispatch)
```

`testbed/cli` is a complete working connector in 72 lines, and exists to keep that claim honest.

**The screen drivers** — serve them from your control plane:

```bash
npm install @micro-teams/connector-applets@0.1.0
```

The package is published to GitHub Packages, so your `.npmrc` needs
`@micro-teams:registry=https://npm.pkg.github.com`. Serve `dist/claude.js` (or `codex.js`) to a
connector as a screen's applet; it is the same file this repository's CI drives a real Claude Code
with.

## Status

0.1.0. One product — MicroTeams — is built on it and runs it in production, with its own end-to-end
tests over the top; a second (CCProxy) is starting. Being 0.x, interfaces may still move: the
driver-declaration shape is the most likely to, once a second consumer's needs are real rather than
anticipated.

The **wire protocol** is versioned separately and is at **1**. It changes only when an older peer
could not survive the message set, which is rarer than a release — see
[`docs/protocol.md`](docs/protocol.md), the specification, with a runnable reference implementation
beside it in [`testbed/server.py`](testbed/server.py).

## License

MIT.
