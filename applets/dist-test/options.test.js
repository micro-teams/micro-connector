// test/options.test.ts
import { test } from "node:test";
import assert from "node:assert/strict";

// src/engine/keys.ts
var ESC = "\x1B";
var UP = ESC + "[A";
var DOWN = ESC + "[B";
var ENTER = "\r";
var PGDN = ESC + "[6~";
var SHIFT_TAB = ESC + "[Z";
var PASTE_START = ESC + "[200~";
var PASTE_END = ESC + "[201~";

// src/engine/options.ts
var clean = (line) => line.replace(/[│╭╮╰╯]/g, "");
function parseOption(line) {
  const m = clean(line).match(/^\s*[❯>]?\s*(\d+)\.\s+(.*\S)\s*$/);
  return m ? { n: parseInt(m[1], 10), label: m[2].trim() } : null;
}
function readOptions(screen) {
  const out = [];
  for (const line of screen.split("\n")) {
    const opt = parseOption(line);
    if (opt) out.push({ opt, selected: /❯/.test(line) });
  }
  return out;
}
function chooseByLabel(write, screen, want) {
  const options = readOptions(screen);
  const target = options.findIndex((o) => want.test(o.opt.label));
  if (target < 0) return "absent";
  const current = options.findIndex((o) => o.selected);
  if (current === target) {
    write(ENTER);
    return "confirmed";
  }
  if (current < 0) return "not-ready";
  const step = target > current ? DOWN : UP;
  for (let i = 0; i < Math.abs(target - current); i++) write(step);
  return "moved";
}

// test/options.test.ts
var gate = (cursorOn) => [
  "  Bypass Permissions mode",
  "",
  `${cursorOn === 1 ? "\u276F" : " "} 1. No, exit`,
  `${cursorOn === 2 ? "\u276F" : " "} 2. Yes, I accept`
].join("\n");
test("an option line is parsed, box drawing and cursor and all", () => {
  assert.deepEqual(parseOption("\u276F 2. Yes, I accept"), { n: 2, label: "Yes, I accept" });
  assert.deepEqual(parseOption("\u2502  1. No, exit  \u2502"), { n: 1, label: "No, exit" });
  assert.equal(parseOption("not an option"), null);
  assert.equal(readOptions(gate(1)).length, 2);
});
test("choosing moves relative to the cursor, so a wrapping list cannot mislead it", () => {
  const w = [];
  assert.equal(chooseByLabel((d) => w.push(d), gate(1), /yes,?\s*i\s*accept/i), "moved");
  assert.deepEqual(w, [DOWN], "one step down: from option 1 to option 2, nothing more");
  const w2 = [];
  assert.equal(chooseByLabel((d) => w2.push(d), gate(2), /yes,?\s*i\s*accept/i), "confirmed");
  assert.deepEqual(w2, [ENTER]);
});
test("moving upward is just as relative", () => {
  const w = [];
  chooseByLabel((d) => w.push(d), gate(2), /no, exit/i);
  assert.deepEqual(w, [UP]);
});
test("an unreadable or absent option is reported, never guessed at", () => {
  const noCursor = gate(0);
  const w = [];
  assert.equal(chooseByLabel((d) => w.push(d), noCursor, /yes/i), "not-ready");
  assert.equal(w.length, 0, "nothing may be typed into a frame we cannot read");
  assert.equal(chooseByLabel((d) => w.push(d), gate(1), /no such option/i), "absent");
  assert.equal(w.length, 0);
});
