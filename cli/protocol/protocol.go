// Package protocol is the message set a connector and a control plane exchange, and the version
// that lets two ends of different ages recognise each other. docs/protocol.md describes every
// message; this package is its Go form.
//
//	Version   the wire version, announced in `hello` (up) and `welcome` (down). It changes only when
//	          an older peer could not survive the message set.
//	Msg       one message: T names its type and only that type's fields are set. Unknown types and
//	          fields are ignored by both ends.
//	Transport what carries messages: Run delivers inbound messages to onMsg until ctx ends; Send may
//	          be called from any goroutine and fails while disconnected.
//
// Example:
//
//	conn.Send(protocol.Msg{T: "screen.data", Sid: "s1", Viewer: "v1", Data: base64Bytes})
package protocol

import "context"

const Version = 2

type Msg struct {
	T   string `json:"t"`
	V   int    `json:"v,omitempty"`
	Sid string `json:"sid,omitempty"`
	ID  string `json:"id,omitempty"`

	// Applet channels: var.set / var.push (Name, Value), rpc.call (ID, Name, Args), rpc.result
	// (ID, Value, Error).
	Name  string `json:"name,omitempty"`
	Value any    `json:"value,omitempty"`
	Args  []any  `json:"args,omitempty"`
	Error string `json:"error,omitempty"`

	// session.create. Adopt marks a create for a screen that may already exist on the machine: a
	// live one is kept, a missing one is reported, never spawned from an empty Command.
	Command []string          `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Screen  string            `json:"screen,omitempty"`
	Source  string            `json:"source,omitempty"`
	Adopt   bool              `json:"adopt,omitempty"`

	// Viewers. Viewer names the viewer a screen.* message comes from or goes to; "" is a single
	// shared viewer. Cols and Rows are a viewer's available size (subscribe, resize) or the
	// window's size (session.create, screen.size). Data is base64: terminal bytes (screen.data,
	// screen.input) or text (screen.capture.result). N is a count of bytes (screen.ack).
	Viewer string `json:"viewer,omitempty"`
	Cols   int    `json:"cols,omitempty"`
	Rows   int    `json:"rows,omitempty"`
	Data   string `json:"data,omitempty"`
	N      int    `json:"n,omitempty"`

	// The input lease. Op is take, renew or release (screen.lease). Holder is who holds it in
	// screen.lease.state: "" when free, "driver" for the applet, otherwise a viewer id.
	Op     string `json:"op,omitempty"`
	Holder string `json:"holder,omitempty"`
}

type Transport interface {
	Run(ctx context.Context, onMsg func(Msg)) error
	Send(m Msg) error
}
