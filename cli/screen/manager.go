// Package screen runs terminal screens on this machine for a control plane.
//
// A screen is a program in a tmux session, driven two ways at once: by its applet (see runtime),
// and by people watching it, each through their own tmux client (see viewers.go). This package
// routes the protocol's messages to both and ascribes no meaning to any of it; what a screen is
// for belongs to the control plane and its applet.
//
// Messages are handled off the transport's read loop: everything for one screen runs in order on
// that screen's own queue, so a slow tmux call delays only its own screen, never the link or the
// other screens.
//
// Screens outlive this process. Detach, what a connector does when it stops or replaces itself,
// leaves every tmux session running; the next process re-adopts them when the control plane sends
// session.create with adopt. Only session.close ends a program.
//
//	NewManager(ctx, conn protocol.Transport, tm *terminal.Manager) *Manager
//	(*Manager).Dispatch(msg protocol.Msg)  handles one inbound message; pass it to conn.Run.
//	(*Manager).Detach()                    stops driving and watching every screen, keeping them.
//	(*Manager).OnScreensChanged func(live int)  told how many sessions tmux has, after each change.
//	(*Manager).OnUpdateRequested func()         called when the control plane sends `update`.
//
// Example:
//
//	mgr := screen.NewManager(ctx, conn, tm)
//	defer mgr.Detach()
//	conn.Run(ctx, mgr.Dispatch)
package screen

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/micro-teams/micro-connector/cli/brand"
	"github.com/micro-teams/micro-connector/cli/protocol"
	"github.com/micro-teams/micro-connector/cli/runtime"
	"github.com/micro-teams/micro-connector/cli/terminal"
)

type Manager struct {
	conn protocol.Transport
	tm   *terminal.Manager
	ctx  context.Context

	mu      sync.Mutex
	screens map[string]*screen
	queues  map[string]*queue

	OnScreensChanged  func(live int)
	OnUpdateRequested func()
}

func NewManager(ctx context.Context, conn protocol.Transport, tm *terminal.Manager) *Manager {
	return &Manager{ctx: ctx, conn: conn, tm: tm, screens: map[string]*screen{}, queues: map[string]*queue{}}
}

// screen is one hosted program and everything attached to it.
type screen struct {
	sid    string
	term   *terminal.Session
	rt     *runtime.Runtime
	cancel context.CancelFunc
	source string // the applet now loaded

	mu      sync.Mutex
	viewers map[string]*viewer
	cols    int
	rows    int
	lease   lease
	inMode  bool // a viewer has sent mouse input since the pane last left copy-mode
}

func (m *Manager) send(msg protocol.Msg) {
	if err := m.conn.Send(msg); err != nil {
		fmt.Fprintf(os.Stderr, "%s: send %s for %s: %v\n", brand.Current.Name, msg.T, msg.Sid, err)
	}
}

func (m *Manager) screen(sid string) *screen {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.screens[sid]
}

func (m *Manager) Dispatch(msg protocol.Msg) {
	switch msg.T {
	case "welcome":
		if msg.V != protocol.Version {
			fmt.Fprintf(os.Stderr, "%s: the control plane speaks protocol %d, this build %d\n",
				brand.Current.Name, msg.V, protocol.Version)
		}
		return
	case "update":
		if m.OnUpdateRequested != nil {
			go m.OnUpdateRequested()
		}
		return
	}
	if msg.Sid == "" {
		fmt.Fprintf(os.Stderr, "%s: %s without a screen id ignored\n", brand.Current.Name, msg.T)
		return
	}
	m.queue(msg.Sid).do(func() { m.handle(msg) })
}

func (m *Manager) handle(msg protocol.Msg) {
	if msg.T == "session.create" {
		m.create(msg)
		return
	}
	s := m.screen(msg.Sid)
	if s == nil {
		if msg.T == "screen.subscribe" {
			m.viewerError(msg.Sid, msg.Viewer, "this machine is not hosting that screen")
		}
		return
	}
	switch msg.T {
	case "session.close":
		m.remove(s)
		if err := s.term.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "%s: close %s: %v\n", brand.Current.Name, s.sid, err)
		}
		m.publishState()
	case "var.set":
		s.rt.SetVar(msg.Name, msg.Value)
	case "rpc.call":
		s.rt.Invoke(msg.ID, msg.Name, msg.Args)
	case "rpc.result":
		s.rt.Resolve(msg.ID, msg.Value, msg.Error)
	case "screen.subscribe":
		m.subscribe(s, msg.Viewer, msg.Cols, msg.Rows)
	case "screen.resize":
		m.resize(s, msg.Viewer, msg.Cols, msg.Rows)
	case "screen.unsubscribe":
		m.unsubscribe(s, msg.Viewer)
	case "screen.input":
		m.input(s, msg.Viewer, msg.Data)
	case "screen.ack":
		m.ack(s, msg.Viewer, msg.N)
	case "screen.lease":
		m.leaseOp(s, msg.Viewer, msg.Op)
	case "screen.capture":
		m.capture(s, msg.Viewer, msg.ID)
	}
}

// create starts a screen, or re-adopts one that is already running.
func (m *Manager) create(msg protocol.Msg) {
	alive, err := m.hasSession(msg.Sid)
	if err != nil {
		m.send(protocol.Msg{T: "session.error", Sid: msg.Sid, Error: err.Error()})
		return
	}
	if s := m.screen(msg.Sid); s != nil {
		if alive {
			// The applet is reloaded only when it changed: a reload starts it from nothing and
			// would drop the messages it is waiting to type.
			if msg.Source != "" && msg.Source != s.source {
				s.source = msg.Source
				s.rt.Load(msg.Source)
			}
			m.send(protocol.Msg{T: "session.ready", Sid: msg.Sid})
			return
		}
		m.remove(s)
	}
	var term *terminal.Session
	switch {
	case alive:
		term = m.tm.Adopt(msg.Sid)
	case msg.Adopt && len(msg.Command) == 0:
		m.send(protocol.Msg{T: "session.error", Sid: msg.Sid, Error: "the screen's program is no longer running on this machine"})
		return
	default:
		env, err := m.environment(msg)
		if err != nil {
			m.send(protocol.Msg{T: "session.error", Sid: msg.Sid, Error: err.Error()})
			return
		}
		if term, err = m.tm.Spawn(msg.Sid, msg.Command, env, msg.Cols, msg.Rows); err != nil {
			m.send(protocol.Msg{T: "session.error", Sid: msg.Sid, Error: err.Error()})
			return
		}
	}
	cols, rows := msg.Cols, msg.Rows
	s := &screen{sid: msg.Sid, term: term, source: msg.Source, viewers: map[string]*viewer{}, cols: cols, rows: rows}
	s.rt = runtime.New(&driverTerm{m: m, s: s}, &bus{m: m, sid: msg.Sid})
	ctx, cancel := context.WithCancel(m.ctx)
	s.cancel = cancel
	m.mu.Lock()
	m.screens[msg.Sid] = s
	m.mu.Unlock()

	term.OnGone(func() {
		m.queue(s.sid).do(func() {
			if m.screen(s.sid) != s {
				return
			}
			m.remove(s)
			m.send(protocol.Msg{T: "session.error", Sid: s.sid, Error: "the screen's tmux session ended"})
			m.publishState()
		})
	})
	go s.rt.Run(ctx)
	if msg.Source != "" {
		s.rt.Load(msg.Source)
	}
	m.send(protocol.Msg{T: "session.ready", Sid: msg.Sid})
	m.publishState()
}

// hasSession asks tmux, retrying a few times: one failed call is a busy tmux, not an answer.
func (m *Manager) hasSession(sid string) (bool, error) {
	var err error
	for i := 0; i < 3; i++ {
		var ok bool
		if ok, err = m.tm.HasSession(sid); err == nil {
			return ok, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false, err
}

// environment is the program's environment: the screen's identity token, a private temp
// directory, then the control plane's variables, which win.
func (m *Manager) environment(msg protocol.Msg) ([]string, error) {
	env := []string{}
	if msg.Screen != "" {
		env = append(env, brand.Current.Env("SCREEN")+"="+msg.Screen)
	}
	// Programs name scratch space after the uid in /tmp (/tmp/claude-$UID), which whoever creates
	// it first owns; a directory of this user's own avoids that race.
	tmp := filepath.Join(brand.Current.RuntimePath(), "tmp", msg.Sid)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, fmt.Errorf("screen temp dir: %w", err)
	}
	env = append(env, "TMPDIR="+tmp)
	for k, v := range msg.Env {
		env = append(env, k+"="+v)
	}
	return env, nil
}

// remove forgets a screen and stops driving and watching it; its tmux session is untouched.
func (m *Manager) remove(s *screen) {
	m.mu.Lock()
	if m.screens[s.sid] == s {
		delete(m.screens, s.sid)
	}
	m.mu.Unlock()
	s.cancel()
	s.term.Stop()
	s.mu.Lock()
	viewers := s.viewers
	s.viewers = map[string]*viewer{}
	s.lease.stop()
	s.mu.Unlock()
	for _, v := range viewers {
		v.client.Close()
	}
}

func (m *Manager) Detach() {
	m.mu.Lock()
	all := make([]*screen, 0, len(m.screens))
	for _, s := range m.screens {
		all = append(all, s)
	}
	m.mu.Unlock()
	for _, s := range all {
		m.remove(s)
	}
}

func (m *Manager) publishState() {
	if m.OnScreensChanged == nil {
		return
	}
	n, err := m.tm.LiveSessions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: count screens: %v\n", brand.Current.Name, err)
		return
	}
	m.OnScreensChanged(n)
}

// bus carries one screen's applet channels as sid-tagged messages.
type bus struct {
	m   *Manager
	sid string
}

func (b *bus) PushVar(name string, value any) {
	b.m.send(protocol.Msg{T: "var.push", Sid: b.sid, Name: name, Value: value})
}

func (b *bus) CallServer(id, name string, args []any) {
	b.m.send(protocol.Msg{T: "rpc.call", Sid: b.sid, ID: id, Name: name, Args: args})
}

func (b *bus) ReplyServer(id string, result any, errStr string) {
	b.m.send(protocol.Msg{T: "rpc.result", Sid: b.sid, ID: id, Value: result, Error: errStr})
}

// queue runs one screen's work in order, on a goroutine that exists only while there is work.
type queue struct {
	mu      sync.Mutex
	jobs    []func()
	running bool
}

func (m *Manager) queue(sid string) *queue {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := m.queues[sid]
	if q == nil {
		q = &queue{}
		m.queues[sid] = q
	}
	return q
}

func (q *queue) do(job func()) {
	q.mu.Lock()
	q.jobs = append(q.jobs, job)
	if q.running {
		q.mu.Unlock()
		return
	}
	q.running = true
	q.mu.Unlock()
	go func() {
		for {
			q.mu.Lock()
			if len(q.jobs) == 0 {
				q.running = false
				q.mu.Unlock()
				return
			}
			job := q.jobs[0]
			q.jobs = q.jobs[1:]
			q.mu.Unlock()
			job()
		}
	}()
}
