// Package protocol is the language a connector and a control plane speak: the message set, and the
// version number that lets two ends of different ages recognise each other.
//
// It is separate from the transport that carries it (transport/ws), so the screen manager and its
// tests depend only on the message set.
package protocol

import "context"

// Version is the wire-protocol version this build speaks. Both ends announce it
// in the opening handshake (`hello` up, `welcome` down), so a mismatch is
// detectable and future revisions can negotiate capabilities without breaking
// older peers. Bump it only for a breaking change.
const Version = 1

// Msg is the union of every field any control message uses. Only the relevant
// ones are set per message type; the rest are omitted.
type Msg struct {
	T       string            `json:"t"`
	V       int               `json:"v,omitempty"` // protocol version (hello / welcome)
	Sid     string            `json:"sid,omitempty"`
	Name    string            `json:"name,omitempty"`
	Value   any               `json:"value,omitempty"`
	ID      string            `json:"id,omitempty"`
	Args    []any             `json:"args,omitempty"`
	Error   string            `json:"error,omitempty"`
	Command []string          `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Screen  string            `json:"screen,omitempty"`
	Cols    int               `json:"cols,omitempty"`
	Rows    int               `json:"rows,omitempty"`
	Source  string            `json:"source,omitempty"`
	// Adopt marks a session.create that re-drives a screen whose tmux session
	// already survives on the device (after a server restart or an in-place update
	// re-exec): the host re-establishes the runtime + driver around the existing
	// tmux instead of spawning a new session. Unknown to older peers (ignored).
	Adopt bool `json:"adopt,omitempty"`
	// Data carries base64-encoded bytes: raw terminal bytes for the direct screen channel
	// (screen.data downstream, screen.input upstream), or a file's content for file.write /
	// file.read.result.
	Data string `json:"data,omitempty"`
	// Dir is the direction of a screen.scroll request ("up" / "down" / "bottom"):
	// the viewer paging through the pane's tmux scrollback (copy-mode). Unknown to
	// older peers (ignored).
	Dir string `json:"dir,omitempty"`
}

// Transport carries the message set. Implementations differ in everything except this: a resident
// WebSocket that reconnects for as long as the machine is up, or an HTTP exchange that lives only
// as long as one command.
//
// Run pumps inbound messages to onMsg until ctx is cancelled. Send is safe to call concurrently and
// may fail while the transport is between connections; a caller that must not lose the message is
// responsible for saying so again.
type Transport interface {
	Run(ctx context.Context, onMsg func(Msg)) error
	Send(m Msg) error
}
