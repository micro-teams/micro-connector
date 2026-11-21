// Package ws carries the protocol over a resident WebSocket that the machine dials out to the control
// plane, so nothing listens on the machine and it works behind NAT. It reconnects with backoff
// (1 s doubling to 5 s) whenever the connection drops, pings every 15 s and treats 45 s without any
// inbound frame as a dead connection, and announces the protocol version (`hello`) on each
// connection. It knows nothing about what the messages mean.
//
// The handshake carries the machine's token in X-<Brand>-Session and the base URL it reached the
// control plane on in X-<Brand>-Origin (<Brand> is brand.Current.Name with a capital initial); the
// control plane echoes the origin to the screens it opens, so a screen never guesses its control
// plane's address.
//
//	New(url, token, origin) *Conn
//	NewWithOptions(url, token, origin, Options) *Conn
//	Options.ChooseURL func() string     the URL for the next attempt (nil: url every time).
//	Options.Report func(url, held, err) how each attempt went; held is how long the connection
//	                                    lasted (0 when the dial failed), since a route that accepts
//	                                    the handshake and drops at once must not count as working.
//	Options.NetDial                     opens the connection an attempt runs over (TLS, when the URL
//	                                    asks for it, goes on top), e.g. a stream through a local
//	                                    multipath client, so a path failing underneath is not a
//	                                    disconnection at all.
//	(*Conn).Run(ctx, onMsg) error       dials and delivers inbound messages until ctx ends.
//	(*Conn).Send(msg) error             safe from any goroutine; fails while disconnected and when a
//	                                    write does not complete within 10 s (the connection is then
//	                                    closed and redialled).
//	(*Conn).Reconnect()                 drops the connection so the next attempt chooses a route
//	                                    afresh; the process and its screens keep running.
//
// Example:
//
//	conn := ws.New("wss://example.com/machine/link", token, "https://example.com")
//	go conn.Run(ctx, mgr.Dispatch)
//	conn.Send(protocol.Msg{T: "var.push", Sid: "s1", Name: "status", Value: "idle"})
package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/micro-teams/micro-connector/cli/brand"
	"github.com/micro-teams/micro-connector/cli/protocol"
)

const (
	pingPeriod = 15 * time.Second
	pongWait   = 45 * time.Second
	writeWait  = 10 * time.Second
)

var _ protocol.Transport = (*Conn)(nil)

type Options struct {
	ChooseURL func() string
	Report    func(url string, held time.Duration, err error)
	NetDial   func(ctx context.Context, network, address string) (net.Conn, error)
}

type Conn struct {
	url    string
	token  string
	origin string
	opts   Options

	mu      sync.Mutex
	conn    *websocket.Conn
	writeMu sync.Mutex
}

func New(url, token, origin string) *Conn {
	return NewWithOptions(url, token, origin, Options{})
}

func NewWithOptions(url, token, origin string, opts Options) *Conn {
	return &Conn{url: url, token: token, origin: origin, opts: opts}
}

func (c *Conn) Reconnect() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (c *Conn) Run(ctx context.Context, onMsg func(protocol.Msg)) error {
	backoff := time.Second
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

// dialer copies the default dialer when NetDial is set, so the shared default is never changed.
func (c *Conn) dialer() *websocket.Dialer {
	if c.opts.NetDial == nil {
		return websocket.DefaultDialer
	}
	d := *websocket.DefaultDialer
	d.NetDialContext = c.opts.NetDial
	return &d
}

func (c *Conn) runOnce(
	ctx context.Context,
	url string,
	onMsg func(protocol.Msg),
) (connected bool, err error) {
	header := http.Header{}
	prefix := "X-" + strings.ToUpper(brand.Current.Name[:1]) + brand.Current.Name[1:]
	if c.token != "" {
		header.Set(prefix+"-Session", c.token)
	}
	if c.origin != "" {
		header.Set(prefix+"-Origin", c.origin)
	}
	conn, _, err := c.dialer().DialContext(ctx, url, header)
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

	if err := c.Send(protocol.Msg{T: "hello", V: protocol.Version}); err != nil {
		return true, err
	}
	if err := conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
		return true, err
	}
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
		if err := conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
			return true, err
		}
		var m protocol.Msg
		if err := json.Unmarshal(data, &m); err != nil {
			fmt.Fprintf(os.Stderr, "%s: unreadable message from the control plane: %v\n", brand.Current.Name, err)
			continue
		}
		onMsg(m)
	}
}

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
	if err := conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		conn.Close()
		return err
	}
	return nil
}
