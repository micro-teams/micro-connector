// Package runtime hosts one screen's applet: a piece of JavaScript from the control plane that drives
// a terminal. It gives the applet exactly three things and ascribes no meaning to any of them:
//
//   - the terminal: read the screen, write input, hold the input lease, hear about changes;
//   - variables: some the applet owns and the control plane mirrors (own), some the control plane
//     owns and the applet observes (watch);
//   - functions: some the applet exposes for the control plane to call (expose), some the control
//     plane answers for the applet (call).
//
// Everything the applet sees happens on one goroutine, a small event loop, so the script runs in a
// consistent world: screen changes, messages from the control plane and resolved calls are all jobs
// on that loop. The script reaches all of it through one global, `connector`, described in api.go.
//
//	New(term Terminal, bus Bus) *Runtime
//	(*Runtime).Run(ctx) error         runs the loop until ctx ends; call it once, in its own goroutine.
//	(*Runtime).Load(source string)    replaces the applet in a fresh VM, dropping the old one's state.
//	(*Runtime).SetVar(name, value)    a control-plane variable changed (delivered to watch listeners).
//	(*Runtime).Invoke(id, name, args) the control plane calls an exposed function; answered on the
//	                                  bus with ReplyServer.
//	(*Runtime).Resolve(id, result, err) the answer to the applet's call with that id.
//
// Example:
//
//	rt := runtime.New(term, bus)
//	go rt.Run(ctx)
//	rt.Load(driverSource)
//	rt.Invoke("1", "say", []any{"hello"})
package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/dop251/goja"
)

// ErrHeld is what a Terminal returns when someone else holds the input lease.
var ErrHeld = errors.New("runtime: the input lease is held by someone else")

// Terminal is the screen the applet drives.
type Terminal interface {
	// Snapshot is the visible screen as text.
	Snapshot() string
	// Write types p into the program. ErrHeld when a person holds the input lease.
	Write(p []byte) error
	// Hold takes (true) or releases (false) the input lease for the applet, so a sequence of
	// writes cannot be interleaved with a person's. ErrHeld when a person holds it.
	Hold(on bool) error
	// OnChange registers fn, called from any goroutine on every screen change and periodically.
	OnChange(fn func())
}

// Bus carries the applet's side of the variable and function channels to the control plane.
type Bus interface {
	PushVar(name string, value any)
	CallServer(id, name string, args []any)
	ReplyServer(id string, result any, errStr string)
}

// Runtime hosts one applet against one terminal and one bus.
type Runtime struct {
	term Terminal
	bus  Bus
	jobs chan func()

	// Loop-only state: touched only by jobs on the loop goroutine.
	vm       *goja.Runtime
	owned    map[string]any
	watched  map[string]*watchedVar
	exposed  map[string]goja.Callable
	pending  map[string]*pendingCall
	callSeq  int
	onChange goja.Callable
}

type watchedVar struct {
	value     any
	listeners []goja.Callable
}

type pendingCall struct {
	resolve func(any) error
	reject  func(any) error
}

func New(term Terminal, bus Bus) *Runtime {
	r := &Runtime{term: term, bus: bus, jobs: make(chan func(), 256)}
	r.reset()
	term.OnChange(func() { r.jobs <- r.fireChange })
	return r
}

func (r *Runtime) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case job := <-r.jobs:
			job()
		}
	}
}

// reset gives the applet a fresh VM. A new VM rather than re-running a script in the old one,
// because a second run of the same top-level declarations fails ("already declared").
func (r *Runtime) reset() {
	r.vm = goja.New()
	r.vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	r.owned = map[string]any{}
	r.watched = map[string]*watchedVar{}
	r.exposed = map[string]goja.Callable{}
	r.pending = map[string]*pendingCall{}
	r.onChange = nil
	r.installAPI()
}

func (r *Runtime) Load(source string) {
	r.jobs <- func() {
		r.reset()
		if _, err := r.vm.RunString(source); err != nil {
			r.logf("applet failed to load: %v", err)
		}
	}
}

func (r *Runtime) SetVar(name string, value any) {
	r.jobs <- func() {
		w := r.watch(name)
		w.value = value
		for _, cb := range w.listeners {
			if _, err := cb(goja.Undefined(), r.vm.ToValue(value)); err != nil {
				r.logf("watch(%s) listener: %v", name, err)
			}
		}
	}
}

func (r *Runtime) Invoke(id, name string, args []any) {
	r.jobs <- func() {
		fn := r.exposed[name]
		if fn == nil {
			r.bus.ReplyServer(id, nil, fmt.Sprintf("the applet exposes no function %q", name))
			return
		}
		vals := make([]goja.Value, len(args))
		for i, a := range args {
			vals[i] = r.vm.ToValue(a)
		}
		res, err := fn(goja.Undefined(), vals...)
		if err != nil {
			r.bus.ReplyServer(id, nil, err.Error())
			return
		}
		r.bus.ReplyServer(id, res.Export(), "")
	}
}

func (r *Runtime) Resolve(id string, result any, errStr string) {
	r.jobs <- func() {
		p := r.pending[id]
		if p == nil {
			return
		}
		delete(r.pending, id)
		var err error
		if errStr != "" {
			err = p.reject(r.vm.ToValue(errStr))
		} else {
			err = p.resolve(r.vm.ToValue(result))
		}
		if err != nil {
			r.logf("resolve call %s: %v", id, err)
		}
	}
}

func (r *Runtime) fireChange() {
	if r.onChange == nil {
		return
	}
	if _, err := r.onChange(goja.Undefined()); err != nil {
		r.logf("onChange handler: %v", err)
	}
}

func (r *Runtime) watch(name string) *watchedVar {
	w := r.watched[name]
	if w == nil {
		w = &watchedVar{}
		r.watched[name] = w
	}
	return w
}
