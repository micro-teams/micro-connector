# testbed

A reference control plane, a connector built only from the library, and the end-to-end test that
makes the two agree.

- `server/` — the executable half of the contract: enrolment, the link, the message set, and a
  test interface for driving screens and reading back what the machine said.
- `connector/` — a connector with no screen handling in it at all, because screen handling is the
  library's job.
- `e2e.sh` — starts both and requires a message to travel from the control plane, through the
  transport, the applet, a pty and a real tmux, into the program's stdin.

Run it: `bash testbed/e2e.sh` (needs node, go, python3 and tmux).
