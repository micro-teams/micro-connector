# testbed

A reference control plane, a connector built only from the library, and the end-to-end test that
makes the two agree.

- `server.py` — the executable half of the contract: enrolment, applet delivery, the message set,
  and somewhere for a machine's messages to arrive. Standard library only; a specification you have
  to install something to read is one people read less.
- `cli/` — a connector with no screen handling in it at all, because screen handling is not a
  product's job. It also proves the one-shot shape works: HTTP polling, no daemon, no WebSocket, no
  service to install.
- `e2e.sh` — starts both and requires a message to travel from the control plane, through the
  transport, the applet, a pty and a real tmux, into the program's stdin.

Run it: `bash testbed/e2e.sh` (needs node, go, python3 and tmux).
