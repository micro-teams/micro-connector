// test/codex.test.ts
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

// test/codex.test.ts
function loadDriver() {
  const host = makeHost();
  runDriver("codex");
  return host;
}
test("the directory-trust gate is answered", () => {
  const host = loadDriver();
  host.frame("Do you trust the contents of this directory?\n\n1. Yes, continue");
  assert.ok(host.keys().includes(ENTER));
});
test("working and idle are read from Codex\u2019s own footer, not Claude\u2019s", () => {
  const host = loadDriver();
  host.frame("\n\u203A \ngpt-5-codex \xB7 ~/work\n");
  assert.equal(host.vars.status, "idle");
  host.frame("\nWorking (6s \xB7 esc to interrupt)\n");
  assert.equal(host.vars.status, "busy");
  assert.equal(host.vars.elapsed, "6s");
});
test("the operator instructions are prepended once, to the first message only", () => {
  const host = loadDriver();
  host.frame("\n\u203A \n");
  host.clearKeys();
  host.exposed.say("first");
  const first = host.keys();
  host.clearKeys();
  host.exposed.say("second");
  const second = host.keys();
  assert.ok(!first.includes("__MT_"), "an un-substituted placeholder must not be sent");
  assert.ok(first.includes("first") && second.includes("second"));
});
