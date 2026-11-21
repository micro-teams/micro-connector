// The applet's view of the runtime: one global object, `connector`.
//
//	connector.term.read(): string           the visible screen
//	connector.term.write(data): boolean     types data; false when refused (a person holds the
//	                                        input lease) or failed (logged)
//	connector.term.hold(on): boolean        takes (true) or releases (false) the input lease; false
//	                                        when a person holds it
//	connector.term.onChange(fn)             fn() on every screen change and every ~1.2 s
//	connector.own(name, initial)            {get(), set(v)}: a variable the control plane mirrors
//	connector.watch(name)                   {get(), onChange(fn)}: a control-plane variable
//	connector.expose(name, fn)              fn may be called by the control plane; its return value
//	                                        is the answer
//	connector.call(name, ...args): Promise  calls the control plane
//	connector.log(message)                  writes to the connector's log
//
// Example:
//
//	const status = connector.own('status', 'starting')
//	connector.term.onChange(() => status.set(/\$ $/.test(connector.term.read()) ? 'idle' : 'busy'))
//	connector.expose('say', (text) => connector.term.write(text + '\r'))
package runtime

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"

	"github.com/dop251/goja"

	"github.com/micro-teams/micro-connector/cli/brand"
)

func (r *Runtime) logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s: applet: %s\n", brand.Current.Name, fmt.Sprintf(format, a...))
}

func (r *Runtime) installAPI() {
	vm := r.vm
	set := func(o *goja.Object, name string, v any) {
		if err := o.Set(name, v); err != nil {
			panic(err) // only fails for a frozen object, which these are not
		}
	}

	term := vm.NewObject()
	set(term, "read", func() string { return r.term.Snapshot() })
	set(term, "write", func(data string) bool {
		err := r.term.Write([]byte(data))
		if err != nil && !errors.Is(err, ErrHeld) {
			r.logf("term.write: %v", err)
		}
		return err == nil
	})
	set(term, "hold", func(on bool) bool {
		err := r.term.Hold(on)
		if err != nil && !errors.Is(err, ErrHeld) {
			r.logf("term.hold: %v", err)
		}
		return err == nil
	})
	set(term, "onChange", func(fn goja.Callable) { r.onChange = fn })

	api := vm.NewObject()
	set(api, "term", term)
	set(api, "own", func(name string, initial goja.Value) *goja.Object {
		r.owned[name] = initial.Export()
		r.bus.PushVar(name, r.owned[name])
		h := vm.NewObject()
		set(h, "get", func() any { return r.owned[name] })
		set(h, "set", func(v goja.Value) {
			x := v.Export()
			if !reflect.DeepEqual(r.owned[name], x) {
				r.owned[name] = x
				r.bus.PushVar(name, x)
			}
		})
		return h
	})
	set(api, "watch", func(name string) *goja.Object {
		w := r.watch(name)
		h := vm.NewObject()
		set(h, "get", func() any { return w.value })
		set(h, "onChange", func(fn goja.Callable) { w.listeners = append(w.listeners, fn) })
		return h
	})
	set(api, "expose", func(name string, fn goja.Callable) { r.exposed[name] = fn })
	set(api, "call", func(call goja.FunctionCall) goja.Value {
		args := []any{}
		for i := 1; i < len(call.Arguments); i++ {
			args = append(args, call.Arguments[i].Export())
		}
		p, resolve, reject := vm.NewPromise()
		r.callSeq++
		id := strconv.Itoa(r.callSeq)
		r.pending[id] = &pendingCall{resolve: resolve, reject: reject}
		r.bus.CallServer(id, call.Argument(0).String(), args)
		return vm.ToValue(p)
	})
	set(api, "log", func(msg string) { r.logf("%s", msg) })
	if err := vm.Set("connector", api); err != nil {
		panic(err)
	}
}
