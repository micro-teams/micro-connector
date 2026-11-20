// Viewers and the input lease of a screen.
//
// Each viewer gets its own tmux client (terminal.Client), so tmux paints it whole when it joins and
// after every resize, and its stream is its own: screen.data carries the viewer's id and goes to
// that viewer only. The window is the smallest available size over the viewers; every client is
// sized to the window and every viewer is told the size with screen.size before any data at that
// size, so it can show the terminal at exactly that size, centred. A viewer acknowledges rendered
// bytes with screen.ack; the viewer "" is a single shared viewer that is never asked to.
//
// Input is arbitrated by a lease with two states, free or held. Input from a viewer on a free
// screen takes it; input or `screen.lease renew` keeps it; it lapses leaseTTL after the last renewal
// or on `release`. Input from anyone else while it is held is refused, and the sender is told who
// holds it with screen.lease.state, which every viewer also receives on each change. The applet
// holds the lease ("driver") around writes that must not be interleaved; its writes are refused
// while a viewer holds it. Scrolling is input too: the viewer's wheel reaches its tmux client as
// mouse sequences, and tmux scrolls its history or passes the wheel to a program that wants it.
//
// screen.capture answers with the screen's text and recent history, for copying on devices where
// selecting inside a terminal is impractical.
package screen

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/micro-teams/micro-connector/cli/brand"
	"github.com/micro-teams/micro-connector/cli/protocol"
	"github.com/micro-teams/micro-connector/cli/runtime"
	"github.com/micro-teams/micro-connector/cli/terminal"
)

const (
	leaseTTL       = 3 * time.Second
	captureHistory = 2000
	driverHolder   = "driver"
)

type viewer struct {
	id     string
	cols   int
	rows   int
	client *terminal.Client
}

// lease is a screen's input lease. Guarded by the screen's mutex.
type lease struct {
	holder string // "" when free
	timer  *time.Timer
}

func (l *lease) stop() {
	if l.timer != nil {
		l.timer.Stop()
	}
}

func (m *Manager) viewerError(sid, viewerID, text string) {
	if viewerID == "" {
		// The shared viewer's control plane knows only screen-wide failure.
		m.send(protocol.Msg{T: "session.error", Sid: sid, Error: text})
		return
	}
	m.send(protocol.Msg{T: "screen.error", Sid: sid, Viewer: viewerID, Error: text})
}

func (m *Manager) subscribe(s *screen, id string, cols, rows int) {
	if cols <= 0 || rows <= 0 {
		m.viewerError(s.sid, id, fmt.Sprintf("viewer size %dx%d", cols, rows))
		return
	}
	m.unsubscribe(s, id)
	v := &viewer{id: id, cols: cols, rows: rows}
	s.mu.Lock()
	s.viewers[id] = v
	s.mu.Unlock()
	m.fit(s)
	s.mu.Lock()
	wc, wr := s.cols, s.rows
	s.mu.Unlock()
	m.send(protocol.Msg{T: "screen.size", Sid: s.sid, Viewer: id, Cols: wc, Rows: wr})
	window := terminal.Window
	if id == "" {
		window = 0
	}
	client, err := s.term.Attach(wc, wr, window, func(b []byte) {
		m.send(protocol.Msg{T: "screen.data", Sid: s.sid, Viewer: id, Data: base64.StdEncoding.EncodeToString(b)})
	}, func(err error) {
		m.queue(s.sid).do(func() {
			s.mu.Lock()
			current := s.viewers[id] == v
			s.mu.Unlock()
			if current {
				m.unsubscribe(s, id)
				m.viewerError(s.sid, id, err.Error())
			}
		})
	})
	if err != nil {
		s.mu.Lock()
		delete(s.viewers, id)
		s.mu.Unlock()
		m.viewerError(s.sid, id, err.Error())
		return
	}
	s.mu.Lock()
	v.client = client
	holder := s.lease.holder
	s.mu.Unlock()
	m.send(protocol.Msg{T: "screen.lease.state", Sid: s.sid, Viewer: id, Holder: holder})
}

func (m *Manager) resize(s *screen, id string, cols, rows int) {
	s.mu.Lock()
	v := s.viewers[id]
	if v != nil && cols > 0 && rows > 0 {
		v.cols, v.rows = cols, rows
	}
	s.mu.Unlock()
	if v != nil {
		m.fit(s)
	}
}

func (m *Manager) unsubscribe(s *screen, id string) {
	s.mu.Lock()
	v := s.viewers[id]
	delete(s.viewers, id)
	released := v != nil && s.lease.holder == id
	if released {
		s.lease.holder = ""
		s.lease.stop()
	}
	s.mu.Unlock()
	if v == nil {
		return
	}
	if v.client != nil {
		v.client.Close()
	}
	m.fit(s)
	if released {
		m.broadcastLease(s)
	}
}

// fit sizes the window to the smallest viewer, then every client to the window, and tells every
// viewer whose size changed. With no viewers the window keeps its size.
func (m *Manager) fit(s *screen) {
	s.mu.Lock()
	cols, rows := 0, 0
	for _, v := range s.viewers {
		if cols == 0 || v.cols < cols {
			cols = v.cols
		}
		if rows == 0 || v.rows < rows {
			rows = v.rows
		}
	}
	if cols == 0 || (cols == s.cols && rows == s.rows) {
		s.mu.Unlock()
		return
	}
	s.cols, s.rows = cols, rows
	viewers := make([]*viewer, 0, len(s.viewers))
	for _, v := range s.viewers {
		viewers = append(viewers, v)
	}
	s.mu.Unlock()
	if err := s.term.Resize(cols, rows); err != nil {
		fmt.Fprintf(os.Stderr, "%s: resize %s: %v\n", brand.Current.Name, s.sid, err)
		return
	}
	for _, v := range viewers {
		if v.client == nil {
			continue
		}
		m.send(protocol.Msg{T: "screen.size", Sid: s.sid, Viewer: v.id, Cols: cols, Rows: rows})
		if err := v.client.Resize(cols, rows); err != nil {
			fmt.Fprintf(os.Stderr, "%s: resize viewer of %s: %v\n", brand.Current.Name, s.sid, err)
		}
	}
}

// take gives the lease to holder if it is free or already theirs, and reports whether it did.
// The caller holds s.mu.
func (m *Manager) take(s *screen, holder string) (granted, changed bool) {
	if s.lease.holder != "" && s.lease.holder != holder {
		return false, false
	}
	changed = s.lease.holder != holder
	s.lease.holder = holder
	s.lease.stop()
	s.lease.timer = time.AfterFunc(leaseTTL, func() {
		m.queue(s.sid).do(func() {
			s.mu.Lock()
			lapsed := s.lease.holder == holder
			if lapsed {
				s.lease.holder = ""
			}
			s.mu.Unlock()
			if lapsed {
				m.broadcastLease(s)
			}
		})
	})
	return true, changed
}

func (m *Manager) broadcastLease(s *screen) {
	s.mu.Lock()
	holder := s.lease.holder
	ids := make([]string, 0, len(s.viewers))
	for id := range s.viewers {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		m.send(protocol.Msg{T: "screen.lease.state", Sid: s.sid, Viewer: id, Holder: holder})
	}
}

func (m *Manager) leaseOp(s *screen, id, op string) {
	s.mu.Lock()
	var granted, changed bool
	switch op {
	case "take", "renew":
		granted, changed = m.take(s, id)
	case "release":
		if s.lease.holder == id {
			s.lease.holder = ""
			s.lease.stop()
			changed = true
		}
		granted = true
	}
	s.mu.Unlock()
	if changed {
		m.broadcastLease(s)
	} else if !granted {
		m.refuse(s, id)
	}
}

func (m *Manager) refuse(s *screen, id string) {
	s.mu.Lock()
	holder := s.lease.holder
	s.mu.Unlock()
	m.send(protocol.Msg{T: "screen.lease.state", Sid: s.sid, Viewer: id, Holder: holder})
}

func (m *Manager) input(s *screen, id, data string) {
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		m.viewerError(s.sid, id, "input is not base64: "+err.Error())
		return
	}
	s.mu.Lock()
	v := s.viewers[id]
	if v == nil || v.client == nil {
		s.mu.Unlock()
		m.viewerError(s.sid, id, "input from a viewer that is not subscribed")
		return
	}
	granted, changed := m.take(s, id)
	mouse := strings.HasPrefix(string(b), "\x1b[<") || strings.HasPrefix(string(b), "\x1b[M")
	leave := !mouse && s.inMode
	if mouse {
		s.inMode = true
	} else if leave {
		s.inMode = false
	}
	s.mu.Unlock()
	if !granted {
		m.refuse(s, id)
		return
	}
	if changed {
		m.broadcastLease(s)
	}
	// Typing after scrolling back means the live screen again: leave copy-mode so the keys reach
	// the program instead of being read as copy-mode commands.
	if leave {
		if err := s.term.LeaveMode(); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %s: %v\n", brand.Current.Name, s.sid, err)
		}
	}
	if err := v.client.Write(b); err != nil {
		m.viewerError(s.sid, id, err.Error())
	}
}

func (m *Manager) ack(s *screen, id string, n int) {
	s.mu.Lock()
	v := s.viewers[id]
	s.mu.Unlock()
	if v != nil && v.client != nil {
		v.client.Ack(n)
	}
}

func (m *Manager) capture(s *screen, id, reqID string) {
	text, err := s.term.Text(captureHistory)
	msg := protocol.Msg{T: "screen.capture.result", Sid: s.sid, Viewer: id, ID: reqID}
	if err != nil {
		msg.Error = err.Error()
	} else {
		msg.Data = base64.StdEncoding.EncodeToString([]byte(text))
	}
	m.send(msg)
}

// driverTerm is the terminal as the applet sees it: the session, behind the input lease.
type driverTerm struct {
	m *Manager
	s *screen
}

func (d *driverTerm) Snapshot() string   { return d.s.term.Snapshot() }
func (d *driverTerm) OnChange(fn func()) { d.s.term.OnChange(fn) }

func (d *driverTerm) Write(p []byte) error {
	d.s.mu.Lock()
	holder := d.s.lease.holder
	d.s.mu.Unlock()
	if holder != "" && holder != driverHolder {
		return runtime.ErrHeld
	}
	return d.s.term.Write(p)
}

func (d *driverTerm) Hold(on bool) error {
	s := d.s
	s.mu.Lock()
	var granted, changed bool
	if on {
		granted, changed = d.m.take(s, driverHolder)
	} else {
		granted = true
		if s.lease.holder == driverHolder {
			s.lease.holder = ""
			s.lease.stop()
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		d.m.queue(s.sid).do(func() { d.m.broadcastLease(s) })
	}
	if !granted {
		return runtime.ErrHeld
	}
	return nil
}
