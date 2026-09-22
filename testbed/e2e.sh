#!/usr/bin/env bash
# The end-to-end test for the testbed: a connector built from the library, a reference control plane,
# a real tmux, and a real applet — talking to each other with nothing faked in between.
#
# What it proves that a unit test cannot: that the pieces agree. The applet is the artifact that
# ships, the protocol is the one in the contract, the terminal is a real tmux session, and the
# control plane is the one anybody implementing this would read. If those four ever stop agreeing,
# this goes red and nothing else does.
#
# One argument picks what plays the program in the terminal, which is also what the CI matrix varies:
#
#   fake            a shell script that records what it is told. No node, no npm, no network — the
#                   deterministic baseline, so a red run reads plainly: if this leg is red, WE broke
#                   something.
#   npm:<version>   a real Claude Code at a pinned version, driven by a mock Anthropic API. Pinned,
#                   so it too can only break when we change something.
#   installer       a real Claude Code, latest. Advisory: when Anthropic ships a change to the
#                   first-run wizard or the permission gates, this is the leg that says so — which
#                   is intelligence worth having rather than a reason to block a merge.
#   pi:<version>    a real pi at a pinned version, driven by a mock OpenAI API. Pinned, for the
#                   same reason as the Claude one.
#
# The real legs assert the thing only they can: that the driver gets a real Claude Code all the way
# to a working prompt, and that a message sent to it is acted on. No AI is involved — the model is a
# mock returning a scripted tool call — so the assertion stays exact.
set -euo pipefail

LEG="${1:-fake}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PORT="${PORT:-8099}"
BASE="http://127.0.0.1:$PORT"
WORK="$(mktemp -d)"
SID="s$(date +%s)"

pass() { printf 'PASS: %s\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }

# What the screen actually shows, asked of the applet itself (it exposes `snapshot`). A failure
# here is nearly always a gate that looks different from what the driver expects, and the pane is
# the only thing that says how.
pane() {
  local id
  id="$(curl -fsS -X POST "$BASE/test/call" -H 'Content-Type: application/json' \
    -d "{\"machine\":\"$MACHINE\",\"sid\":\"$SID\",\"name\":\"snapshot\",\"args\":[]}" |
    python3 -c "import json,sys;print(json.load(sys.stdin)['id'])")"
  sleep 2
  curl -fsS "$BASE/test/state" | python3 -c "
import json,sys
print((json.load(sys.stdin)['rpc'].get('$id') or {}).get('value') or '(no snapshot)')" 2>/dev/null || true
}
step() { printf '\n== %s ==\n' "$1"; }

cleanup() {
  [ -n "${CONN_PID:-}" ] && kill "$CONN_PID" 2>/dev/null || true
  [ -n "${SRV_PID:-}" ] && kill "$SRV_PID" 2>/dev/null || true
  [ -n "${MOCK_PID:-}" ] && kill "$MOCK_PID" 2>/dev/null || true
  # The processes killed above may still be writing under $WORK — the agents' own directories live
  # there — and a removal that races them fails with "Directory not empty". As the last command of
  # an EXIT trap under `set -e`, that failure becomes the SCRIPT's exit code: a run that asserted
  # everything and printed "everything asserted" still ends red. Teardown does not get a vote on
  # the verdict.
  if [ "${KEEP:-0}" != "1" ]; then
    sleep 1
    rm -rf "$WORK" 2>/dev/null || { sleep 2; rm -rf "$WORK" 2>/dev/null || true; }
  fi
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


# --- a real Claude Code, and a model that is not one -----------------------------------------------
# Claude Code is installed for real and started for real; only the model behind it is a mock. That
# split is the whole design: everything this repository is responsible for — the gates, the cursor,
# the paste, the pty — runs exactly as in production, while the part that would make the test slow,
# costly and non-deterministic is replaced by an expectation.
if [ "$LEG" != "fake" ]; then
  export NPM_CONFIG_PREFIX="$WORK/npm"
  export PATH="$NPM_CONFIG_PREFIX/bin:$PATH"

  # MockServer as a plain jar, not a container: Docker Engine does not exist on GitHub-hosted
  # macOS/Windows runners at all (Apple's virtualization terms forbid nesting it, and Windows
  # runners never had it for this use), so a docker-run mock would only ever work on Linux. The
  # shaded jar-with-dependencies is the same artifact the official Docker image itself downloads
  # from Maven Central (see mock-server/mockserver's docker/Dockerfile) — same server, same
  # version, just invoked directly. It needs only a JVM, which every OS here has via
  # actions/setup-java. TLS/tcnative is irrelevant: this test only ever talks to it over plain
  # HTTP on 127.0.0.1:1080.
  #
  # 7.5.0 or newer: `httpLlmResponse` — MockServer's Anthropic emulation — does not exist before
  # it, and an older release answers the expectation with a 400 that says nothing about why.
  MOCK_VERSION=7.5.0
  MOCK_JAR="$WORK/mockserver-netty-$MOCK_VERSION-jar-with-dependencies.jar"
  command -v java >/dev/null || fail "no JVM found — MockServer needs one (see actions/setup-java in CI)"
  curl -fsSL -o "$MOCK_JAR" \
    "https://repo1.maven.org/maven2/org/mock-server/mockserver-netty/$MOCK_VERSION/mockserver-netty-$MOCK_VERSION-jar-with-dependencies.jar"
  # -p, not -serverPort: this is the jar-with-dependencies CLI, invoked exactly as the official
  # Docker image's own build does it (see mock-server/mockserver's docker/Dockerfile) — their docs
  # page shows a different flag/subcommand for the separate -no-dependencies artifact, which is not
  # what this is.
  java -jar "$MOCK_JAR" -p 1080 >"$WORK/mockserver.log" 2>&1 &
  MOCK_PID=$!
  for _ in $(seq 1 60); do
    curl -fsS -X PUT "http://127.0.0.1:1080/mockserver/status" >/dev/null 2>&1 && break
    sleep 1
  done

  if [ "${LEG#pi:}" != "$LEG" ]; then
    # --- pi: a real pi at a pinned version, against a mock OpenAI API ---------------------------
    step "install pi ($LEG) and a mock OpenAI API"
    npm install -g --silent "@earendil-works/pi-coding-agent@${LEG#pi:}" >/dev/null
    command -v pi >/dev/null || fail "pi did not install"
    echo "pi: $(pi --version 2>&1 | head -1)"
    pass "pi installed, mock API up"

    # pi's model is a config file, not an environment variable: one OpenAI-compatible provider
    # pointed at the mock is all a session needs to be usable. Written into this run's own HOME,
    # exactly as the operator's config lives on a real machine.
    mkdir -p "$WORK/home/.pi/agent"
    cat > "$WORK/home/.pi/agent/models.json" <<JSON
{ "providers": { "mtmock": { "name": "Mock OpenAI",
    "baseUrl": "http://127.0.0.1:1080/v1", "api": "openai-completions", "apiKey": "mock",
    "models": [ { "id": "mock-1", "name": "Mock 1", "input": ["text"],
      "contextWindow": 100000, "maxTokens": 4096,
      "cost": { "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0 } } ] } } }
JSON
    # Script the model, the same way as the Claude leg: one once-only expectation that answers
    # with a tool call — streaming, because a non-streamed tool call is silently ignored — and a
    # catch-all that ends the turn.
    MARK="pi-said-$RANDOM"
    REPLY_FILE="$WORK/pi-reply.txt"
    MOCK_PATH="/v1/chat/completions"
    PROG_NAME="pi"
    curl -fsS -X PUT "http://127.0.0.1:1080/mockserver/expectation" \
      -H 'Content-Type: application/json' --data-binary @- >/dev/null <<JSON
{ "httpRequest": { "method": "POST", "path": "/v1/chat/completions" },
  "times": { "remainingTimes": 1, "unlimited": false },
  "priority": 10,
  "httpLlmResponse": { "provider": "OPENAI", "model": "mock-1",
    "completion": { "text": "", "streaming": true, "stopReason": "tool_calls",
      "toolCalls": [ { "id": "call_1", "name": "bash",
        "arguments": "{\"command\":\"echo $MARK > $REPLY_FILE\"}" } ],
      "usage": { "inputTokens": 10, "outputTokens": 5 } } } }
JSON
    curl -fsS -X PUT "http://127.0.0.1:1080/mockserver/expectation" \
      -H 'Content-Type: application/json' --data-binary @- >/dev/null <<'JSON'
{ "httpRequest": { "method": "POST", "path": "/v1/chat/completions" },
  "priority": 1,
  "httpLlmResponse": { "provider": "OPENAI", "model": "mock-1",
    "completion": { "text": "done", "streaming": true, "stopReason": "stop",
                    "usage": { "inputTokens": 10, "outputTokens": 2 } } } }
JSON
  else
    # --- Claude Code, as before ---------------------------------------------------------------
    step "install Claude Code ($LEG) and a mock Anthropic API"
    case "$LEG" in
      npm:*) npm install -g --silent "@anthropic-ai/claude-code@${LEG#npm:}" >/dev/null ;;
      installer) curl -fsSL https://claude.ai/install.sh | bash >/dev/null 2>&1
                 export PATH="$HOME/.local/bin:$PATH" ;;
      *) fail "unknown leg: $LEG" ;;
    esac
    command -v claude >/dev/null || fail "Claude Code did not install"
    echo "claude: $(claude --version 2>&1 | head -1)"
    pass "Claude Code installed, mock API up"

    # Two details here were paid for the hard way:
    #   * the tool list is matched with a JSON path, because Claude Code also asks this endpoint for a
    #     session title, with no tools at all — a once-only expectation is spent on that instead;
    #   * `streaming` is not optional: a non-streamed tool call is silently ignored, which looks
    #     exactly like nothing happening.
    MARK="claude-said-$RANDOM"
    REPLY_FILE="$WORK/claude-reply.txt"
    MOCK_PATH="/v1/messages"
    PROG_NAME="Claude Code"
    curl -fsS -X PUT "http://127.0.0.1:1080/mockserver/expectation" \
    -H 'Content-Type: application/json' --data-binary @- >/dev/null <<JSON
{ "httpRequest": { "method": "POST", "path": "/v1/messages",
                   "body": { "type": "JSON_PATH", "jsonPath": "\$.tools[?(@.name=='Bash')]" } },
  "times": { "remainingTimes": 1, "unlimited": false },
  "priority": 10,
  "httpLlmResponse": { "provider": "ANTHROPIC", "model": "claude-sonnet-4-5",
    "completion": { "text": "Doing it.", "streaming": true, "stopReason": "tool_use",
      "toolCalls": [ { "id": "toolu_mc_1", "name": "Bash",
        "arguments": "{\"command\":\"echo $MARK > $REPLY_FILE\",\"description\":\"reply\"}" } ],
      "usage": { "inputTokens": 100, "outputTokens": 20 } } } }
JSON
  curl -fsS -X PUT "http://127.0.0.1:1080/mockserver/expectation" \
    -H 'Content-Type: application/json' --data-binary @- >/dev/null <<'JSON'
{ "httpRequest": { "method": "POST", "path": "/v1/messages" },
  "priority": 1,
  "httpLlmResponse": { "provider": "ANTHROPIC", "model": "claude-sonnet-4-5",
    "completion": { "text": "done", "streaming": true, "stopReason": "end_turn",
                    "usage": { "inputTokens": 30, "outputTokens": 3 } } } }
JSON
  fi
fi

step "open a screen, and let the applet announce itself"
HEARD="$WORK/heard.txt"; : >"$HEARD"
if [ "$LEG" = "fake" ]; then
  APPLET="claude"
  PROGRAM="while IFS= read -r line; do printf '%s\n' \"\$line\" >> $HEARD; done"
  SCREEN_ENV='{}'
elif [ "${LEG#pi:}" != "$LEG" ]; then
  APPLET="pi"
  # An absolute path, not the name: the program is started through a login shell, which re-reads the
  # profile and rebuilds PATH — so a PATH handed down as screen environment does not survive. The
  # symptom is "Pane is dead (status 127)", which says nothing about why. (Node itself is found by
  # the login shell; only the pi binary lives in this run's npm prefix.)
  PI_BIN="$(command -v pi)"
  PROGRAM="cd $WORK && exec $PI_BIN"
  SCREEN_ENV="{\"HOME\":\"$WORK/home\",\"PATH\":\"$PATH\",\"NO_PROXY\":\"127.0.0.1,localhost\",\"no_proxy\":\"127.0.0.1,localhost\"}"
else
  APPLET="claude"
  # API mode: a base URL and a token are all Claude Code needs to skip login entirely, which is what
  # makes a real Claude Code testable at all without an account. HOME is this run's own, so the
  # first-run wizard and the permission gate appear exactly as they do on a brand-new machine —
  # which is where the gate that used to kill the agent only ever showed up.
  mkdir -p "$WORK/home/.claude"
  printf '{"hasCompletedOnboarding":true}' > "$WORK/home/.claude.json"
  # An absolute path, not the name: the program is started through a login shell, which re-reads the
  # profile and rebuilds PATH — so a PATH handed down as screen environment does not survive. The
  # symptom is "Pane is dead (status 127)", which says nothing about why.
  CLAUDE_BIN="$(command -v claude)"
  PROGRAM="cd $WORK && exec $CLAUDE_BIN --dangerously-skip-permissions"
  # NO_PROXY matters more than it looks: a developer machine often has HTTP(S)_PROXY set, Claude
  # Code honours it, and the mock API is on loopback — so without this the request to the mock goes
  # out through a proxy and comes back as ECONNRESET, which reads on screen as "the model is down"
  # rather than "your environment routed it away".
  SCREEN_ENV="{\"HOME\":\"$WORK/home\",\"ANTHROPIC_BASE_URL\":\"http://127.0.0.1:1080\",\"ANTHROPIC_AUTH_TOKEN\":\"mock\",\"ANTHROPIC_MODEL\":\"claude-sonnet-4-5\",\"PATH\":\"$PATH\",\"NO_PROXY\":\"127.0.0.1,localhost\",\"no_proxy\":\"127.0.0.1,localhost\"}"
fi
python3 - "$MACHINE" "$SID" "$PROGRAM" "$SCREEN_ENV" "$APPLET" > "$WORK/open.json" <<'PY'
import json, sys
machine, sid, program, env, applet = sys.argv[1:6]
print(json.dumps({"machine": machine, "sid": sid, "applet": "" + applet,
                  "command": ["bash", "-lc", program], "env": json.loads(env)}))
PY
curl -fsS -X POST "$BASE/test/open" -H 'Content-Type: application/json' --data-binary @"$WORK/open.json" >/dev/null
for _ in $(seq 1 60); do
  READY="$(curl -fsS "$BASE/test/state" | python3 -c "import json,sys;print(json.load(sys.stdin)['screens'].get('$SID',{}).get('driver',''))")"
  [ "$READY" = "$APPLET" ] && break
  sleep 0.5
done
[ "${READY:-}" = "$APPLET" ] || { cat "$WORK/conn.log"; fail "the applet never announced itself (screenReady)"; }
pass "the screen is live and the applet says it is the $APPLET driver"

if [ "$LEG" != "fake" ]; then
  step "let $PROG_NAME reach a working prompt"
  # Assert on the driver's own opinion rather than on the screen: what is under test is whether it
  # understands what it is looking at, so what it believes is the thing worth checking. A dead
  # status here means a gate was answered wrongly — the failure that killed the first agent on every
  # brand-new machine.
  for _ in $(seq 1 90); do
    ST="$(curl -fsS "$BASE/test/state" | python3 -c "import json,sys;print(json.load(sys.stdin)['screens'].get('$SID',{}).get('vars',{}).get('status',''))")"
    [ "$ST" = "dead" ] && { echo "--- connector ---"; cat "$WORK/conn.log"; echo "--- the pane ---"; pane; fail "$PROG_NAME exited during startup — a gate was answered wrongly"; }
    [ "$ST" = "idle" ] || [ "$ST" = "busy" ] && break
    sleep 1
  done
  case "${ST:-}" in
    idle | busy) pass "the driver got $PROG_NAME through its gates to a working prompt" ;;
    *) echo "--- connector ---"; cat "$WORK/conn.log"; echo "--- the pane ---"; pane
       fail "$PROG_NAME never reached a prompt (status=${ST:-none})" ;;
  esac
fi

step "say something, and require it to have been acted on"
if [ "$LEG" = "fake" ]; then
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
else
  # The assertion only a real Claude Code can make: it ACTED. The message has to be pasted,
  # submitted — an Enter swallowed by the paste leaves a pane that looks perfect and does nothing —
  # taken as a turn, and answered with the scripted tool call. A screenshot could not tell the
  # difference; the file the tool writes can.
  curl -fsS -X POST "$BASE/test/call" -H 'Content-Type: application/json' \
    -d "{\"machine\":\"$MACHINE\",\"sid\":\"$SID\",\"name\":\"say\",\"args\":[\"run the tool\"]}" >/dev/null
  for _ in $(seq 1 90); do [ -s "$REPLY_FILE" ] && { GOT=1; break; }; sleep 1; done
  [ "${GOT:-0}" = "1" ] || {
    echo "--- connector ---"; cat "$WORK/conn.log"
    echo "--- the pane ---"; pane
    echo "--- what the model was asked ---"
    curl -s -X PUT "http://127.0.0.1:1080/mockserver/retrieve?type=REQUESTS&format=JSON" -d "{\"path\":\"$MOCK_PATH\"}" | head -c 1500
    fail "$PROG_NAME never acted on the message (its Enter, or the whole turn, was lost)"
  }
  grep -q "$MARK" "$REPLY_FILE" || fail "the tool ran, but wrote something unexpected"
  pass "a real $PROG_NAME took the message and ran the tool call it was answered with"
fi

step "everything asserted"
