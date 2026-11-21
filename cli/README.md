# cli

The Go library a connector is built from. Each package's file headers document its whole
interface; this is the map.

| package | what it does |
|---|---|
| `brand` | the product's names: binary, environment prefix, config and runtime directories, service name, control plane paths |
| `config` | the machine's config file: control plane base, credential, link URL |
| `auth` | enrolment: start, show the approval link, poll for the credential |
| `apiauth` | an http transport that authenticates requests to the control plane, as the machine or as the screen's user |
| `transport/ws` | the resident WebSocket to the control plane, reconnecting for as long as the process runs |
| `protocol` | the message set (see docs/protocol.md) |
| `terminal` | the private tmux server: sessions, per-viewer clients, sizes, input, text |
| `screen` | screens and viewers on behalf of the control plane: routing, sizing, the input lease, flow control |
| `runtime` | the goja VM an applet runs in, with its terminal, variables and functions |
| `commandapplet` | a command-line tree defined by JavaScript from the control plane |
| `service` | install and control the connector as a service; WSL's systemd |
| `update` | download, verify and swap in a new binary |

## Conventions

- One brand per process, set before anything else runs (`brand.Current`).
- Screens belong to tmux, not to the process: nothing in the library ends a screen except
  `session.close`, `terminal.Session.Close` and `terminal.Manager.KillServer`.
- Everything for one screen runs in order on that screen's queue; nothing blocks the link's read
  loop.
- Tests run their own tmux server under a temporary `HOME`, `TMPDIR` and `XDG_RUNTIME_DIR` and check
  the socket is inside it before anything can kill it.
