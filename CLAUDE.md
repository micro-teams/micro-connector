# Working in this repository

## What this is

A connector that lets a control plane run terminal screens and applets on machines it cannot reach.
Three products share it. The README explains why the expensive part is the screen understanding and
not the Go.

## The rules that exist because something broke

1. **Never move a cursor to an absolute position.** Option lists wrap; counting keystrokes from an
   imagined top of the list is how a driver reliably pressed "No, exit" and killed the agent three
   seconds after launch. Move relative to the cursor you can see, or do nothing this frame.
2. **Never move and confirm in the same frame.** The TUI is still digesting the arrow keys when the
   Enter arrives.
3. **Never write the Enter in the same step as a paste.** It is swallowed into the paste and the
   message sits in the input box looking perfectly fine, unsent.
4. **Never assert on what the screen shows when you can assert on what came back.** A driver that
   has silently stopped working produces a screenshot indistinguishable from one that works.
5. **A Go test must never touch the live tmux socket.** It is a stable per-user path, so a bare
   `NewManager()` in a test connects to whatever connector is running as that user — and these tests
   kill their server when they finish. Once, that killed every agent on a production machine, twice
   in one evening, and cost hours of forensics before the test suite turned out to be the culprit.

## The habit that pays

Push the failing test first, watch CI go red for the exact reason you predicted, then push the fix.
It costs one extra round trip and it is the only way to know the assertion tests anything. Every
assertion in here should have been red once, deliberately.

## Applets

`applets/src/engine` is shared machinery; `applets/src/drivers/*.ts` are declarations about one
program each. Anything that decides HOW to act on a screen belongs to the engine — a declaration
should not be able to express the wrong thing. Resist folding two programs into one declaration:
they share a skeleton, not their gates.

Build output is a self-contained IIFE per driver, because that is what a goja VM is handed.

## Style

Comments explain **why**, especially when the code looks odd — the odd-looking code here is usually
a scar. Say what broke, not just what the code does.
