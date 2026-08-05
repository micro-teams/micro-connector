// Package ws carries the protocol over a resident WebSocket: one connection, dialled out, kept up
// for as long as the machine is.
//
// This is the transport for a product whose screens are long-lived and whose control plane must be
// able to reach them at any moment — a message can arrive at any time, so somebody has to be
// listening. The peer of transport/httppoll, which suits the opposite shape: one command, one
// screen, no daemon.
//
// It always dials out, so it works from behind NAT with nothing listening on the machine, and it
// reconnects with capped backoff and heartbeats while up. It knows nothing about what the messages
// mean.
package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/micro-teams/micro-connector/cli/protocol"
)

// Ping cadence and liveness window. We ping every pingPeriod; the peer's pong
// (or any message) resets a pongWait read deadline. If nothing arrives within
// pongWait — e.g. a half-open connection where the server died but TCP never
// closed — ReadMessage fails and the Run loop reconnects. pingPeriod must be
// comfortably under pongWait.
const (
	pingPeriod = 15 * time.Second
	pongWait   = 45 * time.Second
)

var _ protocol.Transport = (*Conn)(nil)

// Options lets a product decide where each attempt goes, and learn how it went.
//
// The library keeps the reconnect loop — backoff, heartbeats, the meaning of a drop — because that
// is about the protocol. Which of several public routes to dial is not: it is a fact about a
// deployment, and a product that has more than one route needs to choose per attempt and to know
// which attempts held, so it can stop choosing a route that accepts the handshake and then drops
// it. Left zero, this behaves exactly as it always did.
type Options struct {
	// ChooseURL supplies the URL for the next attempt. nil means the URL given to New, every time.
	ChooseURL func() string
	// Report is told how an attempt went: the URL dialled, how long the connection was held (zero
	// if the dial itself failed), and the error if there was one.
	//
	// "How long it was held" rather than "did it connect", because those differ in the case that
	// matters: a route that completes the handshake and severs the connection a second later looks
	// like a success on every attempt, and a client that believed it would reconnect to it forever.
	Report func(url string, held time.Duration, err error)
}

// Conn maintains the dial-out websocket to the control plane.
type Conn struct {
	url    string
	token  string
	origin string // the API base we dialed, reported so the server can echo it to our screens
	opts   Options

	mu      sync.Mutex
	conn    *websocket.Conn
	writeMu sync.Mutex
}

// New builds a Conn dialling url, authenticating with token. origin is the API base this machine
// reached the control plane on (which endpoint it chose); the control plane echoes it back to the
// screens it opens here, so a screen never has to assume its own control plane's address.
func New(url, token, origin string) *Conn {
	return NewWithOptions(url, token, origin, Options{})
}

// NewWithOptions is New with a product's own choice of route per attempt. See Options.
//
// origin stays what the caller passed regardless of which URL an attempt uses: it is the canonical
// address a screen should reach its control plane on, not a record of which route this process
// happens to be dialling at the moment.
func NewWithOptions(url, token, origin string, opts Options) *Conn {
	return &Conn{url: url, token: token, origin: origin, opts: opts}
}

// Reconnect drops the current connection so the loop dials again, choosing where to go afresh.
//
// It exists because the choice of route is made per attempt, which means a connection that is
// working stays where it is — correctly, since dropping a healthy link every time a ranking wobbles
// would be worse than the wobble. But when the set of routes changes, or an operator wants to force
// the question, there has to be a way to ask for a new attempt without stopping the process. That
// distinction is not cosmetic here: stopping this process is what kills the screens it hosts, so
// "reconnect" and "restart" must not be the same button.
//
// Safe to call when nothing is connected: the loop is already dialling.
func (c *Conn) Reconnect() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// Run dials and pumps messages to onMsg until ctx is cancelled, reconnecting
// with capped backoff on any drop.
func (c *Conn) Run(ctx context.Context, onMsg func(protocol.Msg)) error {
	backoff := time.Second
	// Keep the cap low so a machine comes back quickly after a server restart —
	// screens survive the drop locally, so we want to reattach within seconds,
	// not tens of seconds.
	const maxBackoff = 5 * time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		target := c.url
		if c.opts.ChooseURL != nil {
			if chosen := c.opts.ChooseURL(); chosen != "" {
				target = chosen
			}
		}
		started := time.Now()
		connected, err := c.runOnce(ctx, target, onMsg)
		if c.opts.Report != nil {
			held := time.Duration(0)
			if connected {
				held = time.Since(started)
			}
			c.opts.Report(target, held, err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if connected {
			backoff = time.Second
		} else if backoff < maxBackoff {
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
}

func (c *Conn) runOnce(
	ctx context.Context,
	url string,
	onMsg func(protocol.Msg),
) (connected bool, err error) {
	header := http.Header{}
	if c.token != "" {
		header.Set("X-Microteams-Session", c.token)
	}
	if c.origin != "" {
		header.Set("X-Microteams-Origin", c.origin)
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, url, header)
	if err != nil {
		return false, err
	}
	defer conn.Close()

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.conn == conn {
			c.conn = nil
		}
		c.mu.Unlock()
	}()

	_ = c.Send(protocol.Msg{T: "hello", V: protocol.Version})

	// Liveness: reset the read deadline on every pong (and every message below).
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	hbCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go c.pingLoop(hbCtx, conn)

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return true, err
		}
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))
		var m protocol.Msg
		if json.Unmarshal(data, &m) == nil {
			onMsg(m)
		}
	}
}

// pingLoop keeps the connection provably alive: a ping every pingPeriod that the
// server auto-pongs. Writes go through writeMu so they never interleave with a
// Send. A failed write just ends the loop; the read side then errors and Run
// reconnects.
func (c *Conn) pingLoop(ctx context.Context, conn *websocket.Conn) {
	t := time.NewTicker(pingPeriod)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.writeMu.Lock()
			err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			c.writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

var errNotConnected = errors.New("link: not connected")

// Send marshals and writes one message to the server.
func (c *Conn) Send(m protocol.Msg) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errNotConnected
	}
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("link: marshal: %w", err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, data)
}
