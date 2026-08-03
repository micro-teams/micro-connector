# The contract

What a control plane and a connector say to each other, and what each may assume of the other. This
document is the durable half of the specification; `testbed/server.py` is the executable half, and
the two are kept in step by the end-to-end test that runs against it.

Read this if you are implementing a control plane. If you are implementing a connector, use the Go
library — it already speaks all of this.

## The shape of it

A machine dials **out**. Nothing dials in, no port is opened, and everything below travels over one
transport of the control plane's choosing:

- a **resident WebSocket** (`cli/transport/ws`) if screens are long-lived and the control plane must
  reach them at any moment — a message can arrive whenever, so somebody has to be listening;
- **HTTP polling** (`cli/transport/httppoll`) if the work is a single command that drives one screen
  and exits, where a connection to maintain is a liability rather than an asset.

Both are in the library, and a product picks one by handing it to the screen manager.

The message set is identical either way. A transport that changes the messages is a fork, not an
implementation.

Every message is one JSON object with a `t` field naming its type, and only the fields that type
uses. Unknown types must be ignored; unknown fields must be ignored. Both rules exist so an older
connector on somebody's machine keeps working when the control plane learns something new — and
machines are not upgraded in lockstep, ever.

## Version

`v` carries the protocol version, announced by both ends when a connection opens (`hello` up,
`welcome` down). It is currently **1**.

It changes only when the message set changes in a way an older peer cannot survive — which is rarer
than a release, and is why it is not the product's version number. Adding a message type or a field
is not such a change.

## Enrolment

Two calls, both plain HTTP, before any of the above.

```
POST <base>/machine/enroll/start   {}
  -> { "code": "ABC123", "approveUrl": "https://…", "interval": 5 }

POST <base>/machine/enroll/poll    { "code": "ABC123" }
  -> { "status": "pending" }
  -> { "status": "approved", "token": "…", "machineId": "…" }
  -> { "status": "denied" }
```

The connector shows `approveUrl` to whoever is standing there and polls every `interval` seconds
until the answer is not `pending`. What happens on that link — a human approving, a tenant's API
approving, a pre-issued credential being accepted — is the control plane's business entirely.

The path prefix is the product's (`EnrollBase`); the field names above are not negotiable, because
two spellings of one handshake is how a shared component becomes three.

The `token` is the machine's durable credential. It is the *only* thing the machine holds. It must
be revocable on its own, because a machine is exactly the thing you eventually stop trusting.

## Screens

A screen is a program in a terminal on the machine, identified by a control-plane-chosen `sid`.

### Control plane → machine

| `t` | fields | meaning |
|---|---|---|
| `session.create` | `sid`, `command`, `env`, `cols`, `rows`, `source`, `screen`, `adopt` | Open a screen. If a session with this `sid` already exists on the machine, it is **adopted** rather than replaced — see below. `source` is the applet's JavaScript. `screen` is an opaque per-screen token the connector injects into the program's environment. |
| `script.load` | `sid`, `source` | Replace the applet on a live screen. The program keeps running. This is how a fix reaches every machine without shipping a binary. |
| `var.set` | `sid`, `name`, `value` | Set a variable the applet observes. |
| `rpc.call` | `sid`, `id`, `name`, `args` | Call a function the applet exposed. Answered with `rpc.result`. |
| `rpc.result` | `sid`, `id`, `value`, `error` | Answer a call the applet made. **Every call must be answered**, or the applet waits forever. |
| `screen.subscribe` | `sid`, `cols`, `rows` | Attach a viewer: raw terminal bytes start flowing as `screen.data`. |
| `screen.unsubscribe` | `sid` | Detach the viewer. The program is untouched. |
| `screen.input` | `sid`, `data` | Base64 keystrokes from a viewer, straight to the terminal. |
| `screen.scroll` | `sid`, `dir` | `up` / `down` / `bottom` — the viewer paging through scrollback. |
| `screen.resize` | `sid`, `cols`, `rows` | Resize. Viewers re-send their size continuously; the connector only acts on a change. |
| `session.close` | `sid` | Tear the screen down. |
| `exec` | `id`, `command`, `cwd`, `stdin`, `timeout` | Run one command on the machine. Answered with `exec.result`. |
| `exec.cancel` | `id` | Stop an in-flight `exec`. |
| `update` | — | Ask the machine to update its own binary. What that means is the product's. |

### Machine → control plane

| `t` | fields | meaning |
|---|---|---|
| `session.ready` | `sid` | The screen exists. |
| `session.error` | `sid`, `error` | Something went wrong with this screen — **including that its session is gone**. See "A screen that dies". |
| `var.push` | `sid`, `name`, `value` | An applet-owned variable changed. |
| `rpc.call` | `sid`, `id`, `name`, `args` | The applet is calling the control plane. Must be answered with `rpc.result`. |
| `rpc.result` | `sid`, `id`, `value`, `error` | The answer to a `rpc.call` from the control plane. |
| `screen.data` | `sid`, `data` | Base64 terminal bytes for the attached viewer. |
| `exec.result` | `id`, `stdout`, `stderr`, `exit`, `truncated` | The result of an `exec`. `truncated` means output hit the size cap. |

### Adoption, and why it exists

A tmux session outlives the connector process that created it. When a connector restarts — a service
restart, an in-place self-update — the sessions are still there with their programs running.
`session.create` with `adopt` re-establishes the applet and the plumbing **around the existing
session** instead of starting a new one. This is what lets a connector be updated without
interrupting the work running on it.

The connector checks the ground truth (does the session actually exist?) rather than trusting its
own memory, because the failure this prevents is subtle and was shipped once: a stale entry adopted
happily, the applet announced itself, and the control plane marked a screen live that a viewer would
find empty.

### A screen that dies

The applet runtime lives in the connector process, not in the terminal. So it survives the session
it drives: reads return the last screen it saw and writes fail into a log. **Nothing notices unless
something checks.**

The connector therefore confirms and reports: a session that has gone away produces `session.error`,
and a control plane must treat that as "this screen is dead" and rebuild it when it next has reason
to. A control plane that ignores this will show a live screen that nobody can open, forever, and the
only cure will be restarting something by hand.

## What an applet is given

The applet is JavaScript, supplied by the control plane, running on the machine in a sandbox with no
network and no filesystem of its own. It reaches the host through one global — `connector`, with the
product's own name as an alias:

```
connector.term.read() / .write(data) / .onChange(fn)
connector.own(name, initial)   // this applet owns it; the control plane mirrors it
connector.watch(name)          // the control plane owns it; this applet observes it
connector.expose(name, fn)     // the control plane may call this
connector.call(name, args)     // call the control plane; returns a thenable
connector.log(message)
```

A command applet (a product's own command tree, optional) is additionally given `command`, `http`,
`exec`, `fs` and `print`.

That list is short on purpose. Every product-specific feature belongs in an applet, which the
control plane serves and can change today; a new host primitive means shipping a new binary to every
machine, so adding one is a rare and deliberate act. A general capability (`exec` gaining a working
directory) is right; a business-specific one (`gitPush()`) is not.

`onChange` fires on screen changes **and** on a periodic heartbeat, because a dialog nobody is
touching produces no change at all and still has to be answered.

## Rules a control plane must keep

1. **Answer every `rpc.call`.** An unanswered call is an applet that waits forever.
2. **Ignore what you do not recognise** — message types and fields alike.
3. **Treat `session.error` as death**, and rebuild the screen when there is reason to.
4. **Never assume a screen survived a restart**; send `session.create` with `adopt` and let the
   machine tell you which it was.
5. **Serve applets you have tested against the program you are driving.** The connector will run
   whatever you send; it has no opinion about whether it makes sense.

## The reference implementation

`testbed/server.py` implements all of the above in a few hundred lines of standard-library Python,
and `testbed/e2e.sh` drives a real connector against it — including a real Claude Code — on every
change. If this document and that server ever disagree, the server is right and this document is a
bug.
