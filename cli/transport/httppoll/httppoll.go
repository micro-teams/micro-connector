// Package httppoll carries the protocol over ordinary HTTP requests: a long poll for messages
// coming down, a POST for messages going up.
//
// It exists because not every product wants a daemon. MicroTeams keeps a resident WebSocket open
// for as long as the machine is up, because its screens are long-lived and the control plane must
// be able to reach them at any moment. A provisioning tool has the opposite shape: it runs one
// command, drives one screen to completion, and exits. For that, a socket that must be maintained
// is a liability, and two HTTP endpoints are enough.
//
// The protocol above is identical either way. That is the whole point of the transport seam: the
// same applets, the same screen manager, the same messages — carried differently.
package httppoll

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/micro-teams/micro-connector/cli/protocol"
)

// Conn is a protocol.Transport over two endpoints:
//
//	GET  <base>/<inbox>?machine=<id>   long-polls; returns a JSON array of messages (possibly empty)
//	POST <base>/<outbox>?machine=<id>  accepts one message as JSON
//
// A long poll that returns nothing is not an error — it is the normal shape of "nothing happened
// yet". Anything else is retried with a short backoff, because a provisioning run that dies because
// one request lost its way is worse than one that waits a second.
type Conn struct {
	client  *http.Client
	base    string
	inbox   string
	outbox  string
	machine string
	token   string

	writeMu sync.Mutex
}

// New builds a Conn. token, when non-empty, is sent as a bearer credential.
func New(client *http.Client, base, inbox, outbox, machine, token string) *Conn {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &Conn{client: client, base: base, inbox: inbox, outbox: outbox, machine: machine, token: token}
}

var _ protocol.Transport = (*Conn)(nil)

func (c *Conn) url(path string) string {
	return fmt.Sprintf("%s%s?machine=%s", c.base, path, c.machine)
}

func (c *Conn) auth(r *http.Request) {
	if c.token != "" {
		r.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// Run polls for inbound messages until ctx is cancelled, delivering each to onMsg in the order the
// control plane sent them.
func (c *Conn) Run(ctx context.Context, onMsg func(protocol.Msg)) error {
	backoff := 200 * time.Millisecond
	const maxBackoff = 2 * time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msgs, err := c.poll(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		backoff = 200 * time.Millisecond
		for _, m := range msgs {
			onMsg(m)
		}
	}
}

func (c *Conn) poll(ctx context.Context) ([]protocol.Msg, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(c.inbox), nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("httppoll: inbox returned %s", resp.Status)
	}
	var msgs []protocol.Msg
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		return nil, fmt.Errorf("httppoll: inbox: %w", err)
	}
	return msgs, nil
}

// Send posts one message. Serialised, so messages leave in the order they were produced — a screen
// announcing itself before reporting its first variable is not a detail a control plane should have
// to reassemble.
func (c *Conn) Send(m protocol.Msg) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, c.url(c.outbox), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.auth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("httppoll: outbox returned %s", resp.Status)
	}
	return nil
}
