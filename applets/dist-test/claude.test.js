// test/claude.test.ts
import { test } from "node:test";
import assert from "node:assert/strict";

// test/fake-host.ts
import { readFileSync } from "node:fs";
function makeHost() {
  const listeners = [];
  const watchListeners = {};
  const h = {
    screen: "",
    written: [],
    vars: {},
    watched: { viewerLevel: "passive", label: "" },
    exposed: {},
    calls: [],
    term: {
      read: () => h.screen,
      write: (d) => h.written.push(d),
      onChange: (fn) => listeners.push(fn)
    },
    own: (name, initial) => {
      h.vars[name] = initial;
      return { get: () => h.vars[name], set: (v) => h.vars[name] = v };
    },
    watch: (name) => ({
      get: () => h.watched[name],
      onChange: (fn) => (watchListeners[name] ||= []).push(fn)
    }),
    expose: (name, fn) => h.exposed[name] = fn,
    call: (name, args) => {
      h.calls.push({ name, args });
      return { then: () => void 0 };
    },
    log: () => void 0,
    frame: (screen) => {
      if (screen !== void 0) h.screen = screen;
      for (const fn of listeners) fn();
    },
    keys: () => h.written.join(""),
    clearKeys: () => h.written = [],
    setWatched: (name, value) => {
      h.watched[name] = value;
      for (const fn of watchListeners[name] ?? []) fn(value);
    }
  };
  globalThis.connector = h;
  return h;
}
function runDriver(name) {
  const code = readFileSync(new URL(`../dist/${name}.js`, import.meta.url), "utf8");
  new Function(code)();
}

// src/engine/keys.ts
var ESC = "\x1B";
var UP = ESC + "[A";
var DOWN = ESC + "[B";
var ENTER = "\r";
var PGDN = ESC + "[6~";
var SHIFT_TAB = ESC + "[Z";
var PASTE_START = ESC + "[200~";
var PASTE_END = ESC + "[201~";

// test/claude.test.ts
var PASTE_START2 = "\x1B[200~";
function loadDriver() {
  const host = makeHost();
  runDriver("claude");
  return host;
}
var IDLE = ["", "\u256D\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u256E", "\u2502 >            \u2502", "\u2570\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u256F", "? for shortcuts", ""].join("\n");
var BYPASS_GATE = [
  "WARNING: Claude Code running in Bypass Permissions mode",
  "",
  "\u276F 1. No, exit",
  "  2. Yes, I accept",
  "",
  "Enter to confirm"
].join("\n");
var WORKING = ["", "Thinking\u2026 (6m 45s \xB7 \u2193 19.2k tokens)", "esc to interrupt", ""].join("\n");
test("the bypass gate is answered by label, and never with a bare Enter", () => {
  const host = loadDriver();
  host.frame(BYPASS_GATE);
  host.frame(BYPASS_GATE);
  const typed = host.keys();
  assert.ok(!typed.includes(ENTER), 'a bare Enter here answers "No, exit" and kills the agent');
  assert.ok(typed.includes("\x1B[B"), 'it should step down to "Yes, I accept"');
});
test("the folder-trust gate is answered with Enter", () => {
  const host = loadDriver();
  host.frame("Is this a project you created or one you trust?\n\n1. Yes, I trust this folder");
  assert.ok(host.keys().includes(ENTER));
});
test("an idle prompt reads as idle, a working one as busy, with its elapsed and tokens", () => {
  const host = loadDriver();
  host.frame(IDLE);
  assert.equal(host.vars.status, "idle");
  host.frame(WORKING);
  assert.equal(host.vars.status, "busy");
  assert.equal(host.vars.elapsed, "6m45s");
  assert.equal(host.vars.tokens, "19.2k");
  host.frame(IDLE);
  assert.equal(host.vars.elapsed, "", "a finished turn must not leave a stale timer up");
});
test("a message is pasted atomically and submitted on a later frame, never the same one", () => {
  const host = loadDriver();
  host.frame(IDLE);
  host.clearKeys();
  host.exposed.say("hello there");
  const afterSay = host.keys();
  assert.ok(afterSay.startsWith(PASTE_START2), "the body goes in as one bracketed paste");
  assert.ok(
    !afterSay.endsWith(ENTER),
    "an Enter written now is swallowed into the paste \u2014 the message would sit in the box, unsent"
  );
  host.frame(IDLE);
  host.frame(IDLE);
  assert.ok(host.keys().endsWith(ENTER), "and is submitted a couple of frames later");
});
test("nothing is typed while a human holds the keyboard; it is flushed when they let go", () => {
  const host = loadDriver();
  host.frame(IDLE);
  host.setWatched("viewerLevel", "full");
  host.clearKeys();
  assert.equal(host.exposed.say("while you type"), "buffered");
  assert.equal(host.keys(), "", "not one keystroke may land in a human\u2019s input");
  host.setWatched("viewerLevel", "passive");
  assert.ok(host.keys().includes("while you type"));
});
test("a dead pane is reported dead", () => {
  const host = loadDriver();
  host.frame("Pane is dead (status 1)");
  assert.equal(host.vars.status, "dead");
});
test("the driver announces itself so the control plane knows what it is talking to", () => {
  const host = loadDriver();
  assert.equal(host.calls[0].name, "screenReady");
  assert.equal(host.calls[0].args.driver, "claude");
});
