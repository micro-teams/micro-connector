// Tests for the applet runtime with a fake terminal and bus: the API an applet sees, the lease's
// refusals reaching the applet as false, and control-plane variables surviving a reload.
package runtime

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeTerm struct {
	mu      sync.Mutex
	written []string
	held    bool // a person holds the lease
}

func (f *fakeTerm) Snapshot() string { return "screen" }
func (f *fakeTerm) OnChange(func())  {}
func (f *fakeTerm) Write(p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.held {
		return ErrHeld
	}
	f.written = append(f.written, string(p))
	return nil
}
func (f *fakeTerm) Hold(bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.held {
		return ErrHeld
	}
	return nil
}

type fakeBus struct {
	mu      sync.Mutex
	vars    map[string]any
	replies map[string]any
}

func (b *fakeBus) PushVar(name string, v any) { b.mu.Lock(); b.vars[name] = v; b.mu.Unlock() }
func (b *fakeBus) CallServer(string, string, []any) {}
func (b *fakeBus) ReplyServer(id string, v any, errStr string) {
	b.mu.Lock()
	if errStr != "" {
		v = "error: " + errStr
	}
	b.replies[id] = v
	b.mu.Unlock()
}
func (b *fakeBus) reply(id string) (any, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.replies[id]
	return v, ok
}

func start(t *testing.T) (*Runtime, *fakeTerm, *fakeBus) {
	t.Helper()
	term := &fakeTerm{}
	bus := &fakeBus{vars: map[string]any{}, replies: map[string]any{}}
	r := New(term, bus)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go r.Run(ctx)
	return r, term, bus
}

func call(t *testing.T, r *Runtime, bus *fakeBus, id, name string, args ...any) any {
	t.Helper()
	r.Invoke(id, name, args)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := bus.reply(id); ok {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no reply to %s", name)
	return nil
}

func TestTheAppletSeesTheLease(t *testing.T) {
	r, term, bus := start(t)
	r.Load(`connector.expose('w', (s) => connector.term.write(s) && connector.term.hold(true))`)
	if got := call(t, r, bus, "1", "w", "x"); got != true {
		t.Fatalf("write on a free terminal = %v", got)
	}
	term.mu.Lock()
	term.held = true
	term.mu.Unlock()
	if got := call(t, r, bus, "2", "w", "y"); got != false {
		t.Fatalf("write while a person holds the lease = %v", got)
	}
}

func TestVariablesSurviveAReload(t *testing.T) {
	r, _, bus := start(t)
	r.Load(`connector.expose('get', () => connector.watch('mode').get())`)
	r.SetVar("mode", "login")
	r.Load(`connector.expose('get', () => connector.watch('mode').get() + '!')`)
	if got := call(t, r, bus, "1", "get"); got != "login!" {
		t.Fatalf("after a reload the variable reads %v", got)
	}
}

func TestAnUnknownFunctionIsAnError(t *testing.T) {
	r, _, bus := start(t)
	r.Load(``)
	if got := call(t, r, bus, "1", "nope"); got != `error: the applet exposes no function "nope"` {
		t.Fatalf("got %v", got)
	}
}
