#!/usr/bin/env bash
# The end-to-end test for the testbed: a connector built from the library, a reference control plane,
# a real tmux, and a real applet — talking to each other with nothing faked in between.
#
# What it proves that a unit test cannot: that the pieces agree. The applet is the artifact that
# ships, the protocol is the one in the contract, the terminal is a real tmux session, and the
# control plane is the one anybody implementing this would read. If those four ever stop agreeing,
# this goes red and nothing else does.
#
# The program in the terminal is a shell script here, on purpose: this leg is the deterministic
# baseline, so when it is red the cause is ours. The leg that drives a real Claude Code lives beside
# it, and answers a different question — whether Claude Code changed.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PORT="${PORT:-8099}"
BASE="http://127.0.0.1:$PORT"
WORK="$(mktemp -d)"
SID="s$(date +%s)"

pass() { printf 'PASS: %s\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }
step() { printf '\n== %s ==\n' "$1"; }

cleanup() {
  [ -n "${CONN_PID:-}" ] && kill "$CONN_PID" 2>/dev/null || true
  [ -n "${SRV_PID:-}" ] && kill "$SRV_PID" 2>/dev/null || true
  # The connector's tmux is private to its brand, so this cannot reach anyone else's sessions.
  TMPDIR="$WORK" "$ROOT/testbed/bin/testconn" --help >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

step "build the applets and a connector made only of the library"
(cd "$ROOT/applets" && npm ci --silent >/dev/null 2>&1 || npm install --silent >/dev/null 2>&1; npm run build >/dev/null)
mkdir -p "$ROOT/testbed/bin"
(cd "$ROOT/testbed/cli" && go build -o "$ROOT/testbed/bin/testconn" .)
pass "built"

step "start the reference control plane"
APPLET_DIR="$ROOT/applets/dist" PORT="$PORT" python3 "$ROOT/testbed/server.py" >"$WORK/server.log" 2>&1 &
SRV_PID=$!
for _ in $(seq 1 50); do curl -fsS "$BASE/test/state" >/dev/null 2>&1 && break; sleep 0.2; done
curl -fsS "$BASE/test/state" >/dev/null || { cat "$WORK/server.log"; fail "the control plane never came up"; }
pass "control plane up on $PORT"

step "enrol a machine and connect it"
# TMPDIR points at this run's own directory: the tmux socket path is derived from it, so the test
# cannot reach a connector running on the same machine for real. That has gone wrong before.
TMPDIR="$WORK" "$ROOT/testbed/bin/testconn" --base "$BASE" >"$WORK/conn.log" 2>&1 &
CONN_PID=$!
for _ in $(seq 1 50); do grep -q "enrolled as" "$WORK/conn.log" 2>/dev/null && break; sleep 0.2; done
grep -q "enrolled as" "$WORK/conn.log" || { cat "$WORK/conn.log"; fail "the machine never enrolled"; }
MACHINE="$(awk '/enrolled as/{print $3}' "$WORK/conn.log")"
pass "enrolled as $MACHINE"

step "open a screen, and let the applet announce itself"
HEARD="$WORK/heard.txt"; : >"$HEARD"
curl -fsS -X POST "$BASE/test/open" -H 'Content-Type: application/json' -d "$(python3 - <<PY
import json
print(json.dumps({
  "machine": "$MACHINE", "sid": "$SID", "applet": "claude",
  "command": ["bash", "-lc", "while IFS= read -r line; do printf '%s\\n' \"\$line\" >> $HEARD; done"],
}))
PY
)" >/dev/null
for _ in $(seq 1 60); do
  READY="$(curl -fsS "$BASE/test/state" | python3 -c "import json,sys;print(json.load(sys.stdin)['screens'].get('$SID',{}).get('driver',''))")"
  [ "$READY" = "claude" ] && break
  sleep 0.5
done
[ "${READY:-}" = "claude" ] || { cat "$WORK/conn.log"; fail "the applet never announced itself (screenReady)"; }
pass "the screen is live and the applet says it is the claude driver"

step "say something, and require the program to have heard it"
MARK="hello-$RANDOM"
curl -fsS -X POST "$BASE/test/call" -H 'Content-Type: application/json' \
  -d "{\"machine\":\"$MACHINE\",\"sid\":\"$SID\",\"name\":\"say\",\"args\":[\"$MARK\"]}" >/dev/null
for _ in $(seq 1 60); do grep -q "$MARK" "$HEARD" 2>/dev/null && { GOT=1; break; }; sleep 0.5; done
[ "${GOT:-0}" = "1" ] || {
  echo "--- connector ---"; cat "$WORK/conn.log"
  echo "--- what the program heard ---"; cat "$HEARD"
  fail "the message never reached the program in the terminal"
}
pass "the message crossed the control plane, the transport, the applet, the pty and tmux"

step "everything asserted"
