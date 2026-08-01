#!/usr/bin/env python3
"""A reference control plane for micro-connector: the executable half of the contract.

Everything a control plane must provide is here and nothing else is, which makes this the shortest
honest answer to "what do I have to implement?" — enrolment, applet delivery, the message set, and
somewhere for a machine's messages to arrive. It is also the other end of the end-to-end tests, so
it cannot drift into being a description of a contract nobody speaks.

Python standard library only, on purpose: a specification you need to install something to read is
one people will read less.

  POST /machine/enroll/start           -> {"code": ..., "approveUrl": ..., "interval": ...}
  POST /machine/enroll/poll            -> {"status": "approved", "machineId": ..., "token": ...}
  GET  /bus/inbox?machine=<id>         -> [ ...messages for the machine... ]   (long poll)
  POST /bus/outbox?machine=<id>        <- one message from the machine
  GET  /applet/<name>.js               -> the screen applet's source

  POST /test/open                      -> open a screen (test control)
  POST /test/say                       -> call an applet function on a screen
  GET  /test/state                     -> everything the machine has told us
"""
import json
import os
import queue
import sys
import threading
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs

APPLET_DIR = os.environ.get("APPLET_DIR", "applets/dist")
PORT = int(os.environ.get("PORT", "8099"))
POLL_SECONDS = 10

# What the machine has told us. A control plane would keep this in a database; the point here is
# that everything below is derived from messages, never assumed.
state = {
    "machines": {},          # machineId -> {"token": ...}
    "screens": {},           # sid -> {"ready": bool, "driver": ..., "vars": {...}}
    "rpc": {},               # id -> result the applet returned
    "messages": [],          # every message received, in order, for assertions
}
outbound = {}                # machineId -> queue of messages to deliver
lock = threading.Lock()


def outbox_for(machine):
    with lock:
        return outbound.setdefault(machine, queue.Queue())


def on_machine_message(machine, m):
    """The whole control-plane side of the protocol, in one place."""
    with lock:
        state["messages"].append(m)
    t = m.get("t")
    sid = m.get("sid")
    if t == "session.ready":
        state["screens"].setdefault(sid, {})["ready"] = True
    elif t == "session.error":
        state["screens"].setdefault(sid, {})["error"] = m.get("error")
    elif t == "var.push":
        state["screens"].setdefault(sid, {}).setdefault("vars", {})[m.get("name")] = m.get("value")
    elif t == "rpc.call":
        # The applet is calling us. `screenReady` is the one call a driver makes on startup to say
        # what it is; anything else a control plane defines for itself. Every call must be answered,
        # or the applet waits forever.
        if m.get("name") == "screenReady":
            args = m.get("args") or [{}]
            state["screens"].setdefault(sid, {})["driver"] = (args[0] or {}).get("driver")
        outbox_for(machine).put({"t": "rpc.result", "sid": sid, "id": m.get("id"), "value": {"ok": True}})
    elif t == "rpc.result":
        state["rpc"][m.get("id")] = {"value": m.get("value"), "error": m.get("error")}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *a):  # quiet: the test's output is the interesting one
        pass

    # --- helpers ---------------------------------------------------------------
    def _read_json(self):
        n = int(self.headers.get("Content-Length") or 0)
        return json.loads(self.rfile.read(n) or b"{}")

    def _send(self, obj, status=200, raw=None, ctype="application/json"):
        body = raw if raw is not None else json.dumps(obj).encode()
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _machine(self):
        return (parse_qs(urlparse(self.path).query).get("machine") or [""])[0]

    # --- the contract ----------------------------------------------------------
    def do_POST(self):
        path = urlparse(self.path).path
        # Always drain the body, even when the handler does not want it: on a keep-alive connection
        # an unread body is read as the start of the next request, and the error that produces
        # ("Unsupported method") points nowhere near the cause.
        body = self._read_json()
        if path == "/machine/enroll/start":
            # The shape the connector's own enrolment expects: a code to show, a link for whoever
            # approves it, and how often to poll.
            code = uuid.uuid4().hex[:6].upper()
            return self._send({"code": code, "approveUrl": f"http://127.0.0.1:{PORT}/approve/{code}",
                               "interval": 1})
        if path == "/machine/enroll/poll":
            # A real control plane waits for a human or a tenant to approve. The reference one
            # approves immediately: what is being specified here is the shape of the exchange, not
            # anyone's approval policy.
            mid = "m-" + uuid.uuid4().hex[:8]
            token = uuid.uuid4().hex
            state["machines"][mid] = {"token": token}
            return self._send({"status": "approved", "machineId": mid, "token": token})
        if path == "/bus/outbox":
            on_machine_message(self._machine(), body)
            return self._send({"ok": True})
        if path == "/test/open":
            outbox_for(body["machine"]).put({
                "t": "session.create",
                "sid": body["sid"],
                "command": body.get("command") or ["bash", "-lc", "cat"],
                "source": open(os.path.join(APPLET_DIR, body.get("applet", "claude") + ".js")).read(),
                "cols": body.get("cols", 120),
                "rows": body.get("rows", 32),
                "env": body.get("env") or {},
            })
            return self._send({"ok": True})
        if path == "/test/call":
            rid = uuid.uuid4().hex[:8]
            outbox_for(body["machine"]).put({
                "t": "rpc.call", "sid": body["sid"], "id": rid,
                "name": body["name"], "args": body.get("args") or [],
            })
            return self._send({"id": rid})
        return self._send({"error": "not found"}, 404)

    def do_GET(self):
        path = urlparse(self.path).path
        if path == "/bus/inbox":
            q = outbox_for(self._machine())
            msgs = []
            try:
                msgs.append(q.get(timeout=POLL_SECONDS))
            except queue.Empty:
                pass
            while True:  # drain whatever else is waiting, so ordering is preserved
                try:
                    msgs.append(q.get_nowait())
                except queue.Empty:
                    break
            return self._send(msgs)
        if path.startswith("/applet/"):
            name = os.path.basename(path)
            try:
                with open(os.path.join(APPLET_DIR, name), "rb") as f:
                    return self._send(None, raw=f.read(), ctype="application/javascript")
            except FileNotFoundError:
                return self._send({"error": "no such applet"}, 404)
        if path == "/test/state":
            with lock:
                return self._send({k: v for k, v in state.items() if k != "messages"} |
                                  {"messageCount": len(state["messages"])})
        return self._send({"error": "not found"}, 404)


if __name__ == "__main__":
    srv = ThreadingHTTPServer(("0.0.0.0", PORT), Handler)
    print(f"reference control plane on :{PORT}, applets from {APPLET_DIR}", flush=True)
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        sys.exit(0)
