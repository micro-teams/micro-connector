// Package screen runs terminal screens on this machine on behalf of a control plane.
//
// A screen is a program in a terminal, driven two ways at once: through a hosted script (the
// applet) over a variable/function bus, and directly, as a raw byte stream to and from the terminal
// for a human watching it. This package wires those together and ascribes no meaning to any of it —
// what a screen is FOR belongs to the control plane and its applets.
//
// It is deliberately unaware of how it is reached. Messages arrive from a protocol.Transport, which
// may be a resident WebSocket that lives as long as the machine or an HTTP exchange that lives as
// long as one command, and the code below cannot tell the difference.
package screen

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/micro-teams/micro-connector/cli/brand"
	"github.com/micro-teams/micro-connector/cli/protocol"
	"github.com/micro-teams/micro-connector/cli/runtime"
	"github.com/micro-teams/micro-connector/cli/terminal"
)

// scrollStep is how many scrollback lines one viewer scroll message moves. The
// browser coalesces wheel/touch into discrete up/down messages, so a small step
// gives a smooth, wheel-like feel while paging through tmux copy-mode history.
const scrollStep = 3

// Manager owns the live screens on this machine and the transport they are driven over.
type Manager struct {
	conn protocol.Transport
	tm   *terminal.Manager

	ctx      context.Context
	mu       sync.Mutex
	sessions map[string]*sess

	execMu sync.Mutex
	execs  map[string]context.CancelFunc // in-flight exec id -> cancel

	// The two things a product, rather than a screen manager, decides.
	//
	// OnScreensChanged reports how many screens tmux actually has, whenever that changes; a product
	// may publish it where its own CLI can read it. Counting what tmux has rather than what the map
	// remembers is the point: a map entry outlives its session, and after a tmux server died the
	// old count went on claiming live screens nobody could open.
	//
	// OnUpdateRequested fires when the control plane asks this machine to update itself. What
	// updating means — where the binary comes from, whether running screens survive it — belongs to
	// the product.
	OnScreensChanged  func(live int)
	OnUpdateRequested func()
}

// NewManager builds a screen manager on a transport and a tmux manager. Nothing starts until the
// control plane says so: the manager only ever reacts to messages handed to Dispatch.
func NewManager(ctx context.Context, conn protocol.Transport, tm *terminal.Manager) *Manager {
	return &Manager{
		ctx:      ctx,
		conn:     conn,
		tm:       tm,
		sessions: map[string]*sess{},
		execs:    map[string]context.CancelFunc{},
	}
}

// publishState reports the number of screens tmux really has to whoever asked to know.
func (m *Manager) publishState() {
	if m.OnScreensChanged == nil {
		return
	}
	m.mu.Lock()
	sids := make([]string, 0, len(m.sessions))
	for sid := range m.sessions {
		sids = append(sids, sid)
	}
	m.mu.Unlock()
	n := 0
	for _, sid := range sids {
		if m.tm.HasSession(sid) {
			n++
		}
	}
	m.OnScreensChanged(n)
}

// CloseAll tears every screen down. The tmux sessions themselves are left alone: whether a screen's
// program should outlive this process is decided where the process ends, not here.
func (m *Manager) CloseAll() { m.closeAll() }

// CloseViewerClients detaches every attached viewer without touching the programs — what a process
// about to replace itself must do, so no orphan tmux client is left fighting the next one.
func (m *Manager) CloseViewerClients() { m.closeViewerClients() }

type sess struct {
	term     *terminal.Session
	rt       *runtime.Runtime
	cancel   context.CancelFunc
	client   *terminal.Client // a real tmux client (pty) while a viewer is attached
	lastCols int
	lastRows int
}

func (m *Manager) Dispatch(msg protocol.Msg) {
	switch msg.T {
	case "welcome":
		if msg.V != 0 && msg.V != protocol.Version {
			fmt.Fprintf(os.Stderr, "%s: protocol version mismatch (control plane %d, this build %d) — update %s if things misbehave\n",
				brand.Current.Name, msg.V, protocol.Version, brand.Current.Name)
		}
	case "session.create":
		m.createSession(msg)
	case "session.close":
		m.closeSession(msg.Sid)
	case "script.load":
		if s := m.session(msg.Sid); s != nil {
			s.rt.LoadScript(msg.Source)
		}
	case "var.set": // server-owned variable pushed down
		if s := m.session(msg.Sid); s != nil {
			s.rt.SetVar(msg.Name, msg.Value)
		}
	case "rpc.call": // server invokes a script-exposed function
		if s := m.session(msg.Sid); s != nil {
			s.rt.Invoke(msg.ID, msg.Name, msg.Args)
		}
	case "rpc.result": // result of a script->server call
		if s := m.session(msg.Sid); s != nil {
			s.rt.Resolve(msg.ID, msg.Value, msg.Error)
		}
	case "screen.subscribe": // attach a real tmux client sized to the viewer
		m.subscribeScreen(msg.Sid, msg.Cols, msg.Rows)
	case "screen.unsubscribe":
		m.unsubscribeScreen(msg.Sid)
	case "screen.input": // raw viewer keystrokes -> the client's pty
		if s := m.session(msg.Sid); s != nil && s.client != nil {
			if b, err := base64.StdEncoding.DecodeString(msg.Data); err == nil {
				// Typing means "back to the live program": leave any copy-mode
				// scroll first, so the keystroke reaches the program instead of
				// being interpreted as a copy-mode command.
				s.term.ExitCopyMode()
				_ = s.client.Write(b)
			}
		}
	case "screen.scroll": // viewer pages through the pane's tmux scrollback (copy-mode)
		if s := m.session(msg.Sid); s != nil {
			switch msg.Dir {
			case "up":
				s.term.ScrollUp(scrollStep)
			case "down":
				s.term.ScrollDown(scrollStep)
			default: // "bottom" / anything else: return to the live screen
				s.term.ExitCopyMode()
			}
		}
	case "exec": // run a one-shot command on this machine and return its output
		go m.runExec(msg)
	case "exec.cancel": // stop an in-flight exec (e.g. the caller's timeout fired)
		m.cancelExec(msg.ID)
	case "file.write": // structured, shell-free file write — see protocol.Msg's Path doc comment
		go m.fileWrite(msg)
	case "file.read":
		go m.fileRead(msg)
	case "file.remove":
		go m.fileRemove(msg)
	case "homedir":
		go m.homeDir(msg)
	case "update": // the control plane asks this machine to update itself
		if m.OnUpdateRequested != nil {
			go m.OnUpdateRequested()
		}
	case "screen.resize":
		if s := m.session(msg.Sid); s != nil {
			// Viewers re-send their size continuously (and on a timer) to keep the
			// real terminal matched to what they render. Resize only on an actual
			// change, so same-size pings don't make the program repaint.
			m.mu.Lock()
			changed := msg.Cols != s.lastCols || msg.Rows != s.lastRows
			s.lastCols, s.lastRows = msg.Cols, msg.Rows
			client := s.client
			m.mu.Unlock()
			if changed && client != nil {
				_ = client.Resize(msg.Cols, msg.Rows)
			}
		}
	}
}

func (m *Manager) session(sid string) *sess {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[sid]
}

func (m *Manager) createSession(msg protocol.Msg) {
	if existing := m.session(msg.Sid); existing != nil {
		// "We have an entry for it" is not the same as "it is still there". The tmux server can
		// die under us — and when it does, nothing here notices: the applet runtime lives in THIS
		// process, and reading a dead session's screen just returns "" (see terminal.Read), so the
		// runtime keeps polling happily forever. Taking the shortcut below on that stale entry is
		// what made a dead screen look healthy: the adopt hot-reloaded the applet, the applet
		// announced itself with screenReady, and the server marked the screen LIVE again — while
		// `tmux ls` reported no server at all and every viewer opened onto nothing.
		//
		// So check the ground truth first. A stale entry is torn down and we fall through, which
		// either respawns the session (a create carrying a command) or reports session.error (a
		// pure adopt, whose empty command cannot spawn) — and either outcome tells the server the
		// truth instead of confirming a ghost.
		if m.tm.HasSession(msg.Sid) {
			// Same-process reconnect: the screen is genuinely live locally. An adopt create
			// carries the current driver source — hot-reload it into the live runtime (this is how
			// a backend restart pushes the latest applet into already-running screens). A non-adopt
			// duplicate is a harmless no-op.
			if msg.Adopt && msg.Source != "" {
				existing.rt.LoadScript(msg.Source)
			}
			return
		}
		m.closeSession(msg.Sid)
	}
	// The server owns the screen's identity: it hands down an opaque token in
	// msg.Screen, which the host injects as MICROTEAMS_SCREEN so any process the screen
	// spawns can prove which screen it belongs to when it calls back.
	env := make([]string, 0, len(msg.Env)+2)
	if msg.Screen != "" {
		env = append(env, brand.Current.Env("SCREEN")+"="+msg.Screen)
	}
	// A temp directory of this user's own, so what the hosted program scratches down is not in
	// /tmp. Programs name their scratch space after the uid — Claude Code writes /tmp/claude-$UID —
	// and /tmp is world-writable, so that name is a race: whoever creates it first owns it, and on
	// a tmpfs /tmp the race is re-run at every boot. The morning this was written, root had won it,
	// and the agent died about a minute after starting, every time.
	//
	// Set before msg.Env so a server that has an opinion about TMPDIR still wins.
	if tmp, err := screenTmpDir(msg.Sid); err == nil {
		env = append(env, "TMPDIR="+tmp)
	}
	for k, v := range msg.Env {
		env = append(env, k+"="+v)
	}

	// If a tmux session for this sid already exists (it survived an in-place update
	// re-exec or a server restart), ADOPT it: re-establish the runtime + driver +
	// polling around the still-running program instead of spawning a new session.
	// Otherwise spawn a fresh tmux session + program as usual. The server sets
	// msg.Adopt on the re-provision path; HasSession is the ground truth we act on.
	var term *terminal.Session
	if m.tm.HasSession(msg.Sid) {
		term = m.tm.Adopt(msg.Sid)
	} else {
		var err error
		term, err = m.tm.Spawn(msg.Sid, msg.Command, env, msg.Cols, msg.Rows)
		if err != nil {
			_ = m.conn.Send(protocol.Msg{T: "session.error", Sid: msg.Sid, Error: err.Error()})
			return
		}
	}
	rt := runtime.New(term, &busAdapter{conn: m.conn, sid: msg.Sid})
	ctx, cancel := context.WithCancel(m.ctx)

	m.mu.Lock()
	m.sessions[msg.Sid] = &sess{term: term, rt: rt, cancel: cancel}
	m.mu.Unlock()

	// If this session dies while we are driving it, say so. Until the server is told, it keeps the
	// screen LIVE — so it never respawns it and never queues anything for it, and messages sent to
	// the agent are typed into a terminal that is not there any more (they end up in the connector
	// log as failed writes, which no user reads). `session.error` is the same report a failed adopt
	// makes, so the server's existing path takes it from here: mark the screen dead, then rebuild it
	// the next time someone writes to the agent or opens its live screen.
	sid := msg.Sid
	term.OnGone(func() {
		fmt.Fprintf(os.Stderr, "%s: screen %s: its tmux session is gone; reporting it dead\n", brand.Current.Name, sid)
		m.closeSession(sid)
		_ = m.conn.Send(protocol.Msg{T: "session.error", Sid: sid, Error: "terminal: session is gone"})
		m.publishState()
	})

	go func() { _ = rt.Run(ctx) }()

	if msg.Source != "" {
		rt.LoadScript(msg.Source)
	}
	_ = m.conn.Send(protocol.Msg{T: "session.ready", Sid: msg.Sid})
	m.publishState()
}

func (m *Manager) subscribeScreen(sid string, cols, rows int) {
	s := m.session(sid)
	// Somebody is waiting on the other end of this, so not hosting it has to be SAID. The server
	// believes a screen is live until told otherwise; staying quiet leaves it believing that while
	// a person watches a terminal that never opens — the exact shape of the report this came from:
	// "microteams and the CLI both say connected, but the screen cannot actually be opened".
	//
	// It happens whenever the sessions died without this process watching them go: a stop kills the
	// tmux server (`link disconnect` does), and a reboot takes it with /tmp. Reconnecting brings the
	// link back but not the sessions, and the server's records outlive them.
	//
	// `session.error` is the report a failed adopt already makes, so the server's existing path
	// takes it from here: mark the screen dead, then rebuild it the next time somebody opens it.
	if s == nil {
		_ = m.conn.Send(protocol.Msg{T: "session.error", Sid: sid,
			Error: "terminal: this machine is not hosting that screen"})
		return
	}
	if s.client != nil {
		// Somebody is already attached, so tmux will not do us the favor a fresh `attach-session`
		// does automatically: it emits a full clear+repaint only for the client that causes the
		// attach, and the transport doesn't let a second real tmux client share a pty here (the
		// hub sends only ONE screen.subscribe per NEW viewer, but there is one already-open pty
		// for the whole screen — see MachineHub.attachViewer on the backend). Without something
		// here, a second (or later) viewer is added to the hub's fan-out list and then simply waits
		// for the *next* incidental change to paint over an initially blank/wrong local buffer —
		// which is the exact gap this fix closes; see also ViewerPump's file header on the backend.
		//
		// So synthesize what a real attach would have sent: capture the pane as it stands right
		// now, with color intact, and wrap it the same way a genuine attach frames a repaint —
		// clear+home first, cursor placed and made visible last.
		m.sendSyntheticSnapshot(sid, s)
		return
	}
	// Having a record of the screen is not the same as the screen being there. tmux can die while
	// this process lives — a `tmux kill-server`, an OOM, anything that takes the server without
	// taking us — and Attach would not notice: it forks a pty and starts `tmux attach-session` in
	// it, which SUCCEEDS as a fork even though the tmux inside it exits immediately with "no such
	// session". A viewer then gets a pty that dies quietly, and still nobody has said anything.
	//
	// So ask tmux, which is the only thing that knows, before handing anybody a terminal.
	if !m.tm.HasSession(sid) {
		m.closeSession(sid)
		_ = m.conn.Send(protocol.Msg{T: "session.error", Sid: sid,
			Error: "terminal: that screen's session is gone"})
		m.publishState()
		return
	}
	// A fresh viewer always starts on the live screen: clear any copy-mode a
	// previous viewer left behind on the pane.
	s.term.ExitCopyMode()
	client, err := s.term.Attach(cols, rows, func(b []byte) {
		_ = m.conn.Send(protocol.Msg{T: "screen.data", Sid: sid,
			Data: base64.StdEncoding.EncodeToString(b)})
	})
	if err != nil {
		_ = m.conn.Send(protocol.Msg{T: "session.error", Sid: sid, Error: err.Error()})
		return
	}
	m.mu.Lock()
	s.client = client
	s.lastCols, s.lastRows = cols, rows
	m.mu.Unlock()
}

// sendSyntheticSnapshot reconstructs what a real `tmux attach-session` would have painted for a
// viewer who cannot get one, because this screen already has a client attached (see the call site
// in subscribeScreen for why real tmux cannot just hand out a second one here).
//
// Sent as an ordinary screen.data message, this is indistinguishable on the wire from a real
// attach's output — and the backend's fan-out is not addressed to one viewer, so EVERY existing
// viewer of this screen receives it too. For a viewer who already has a correct picture that is a
// harmless, rare redraw flash — nothing here is wrong for them, just repainted — which is a good
// trade for a viewer who would otherwise stay on a blank or stale screen indefinitely. There is no
// finer-grained addressing in the current wire protocol to avoid it.
func (m *Manager) sendSyntheticSnapshot(sid string, s *sess) {
	body, err := s.term.CaptureANSI()
	if err != nil {
		// Nothing to send is no worse than today's silent nothing — and unlike subscribeScreen's
		// other failure paths, the screen IS live (a client is attached to it right now), so this
		// is not something the server needs telling about.
		return
	}
	col, row, err := s.term.CursorPosition()
	if err != nil {
		col, row = 0, 0
	}
	var b strings.Builder
	b.WriteString("\x1b[H\x1b[J") // clear + home, matching what a real attach sends first
	// capture-pane -p joins lines with a bare "\n"; a terminal only moves down on that unless
	// line-feed mode is on (it is not, by default), so each join has to carry its own "\r" too, or
	// every line after the first renders stacked at the same column instead of on its own row.
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	fmt.Fprintf(&b, "\x1b[%d;%dH", row+1, col+1) // tmux's cursor_x/y are 0-indexed; CUP is 1-indexed
	b.WriteString("\x1b[?25h")                   // cursor visible, matching what a real attach ends with
	_ = m.conn.Send(protocol.Msg{T: "screen.data", Sid: sid,
		Data: base64.StdEncoding.EncodeToString([]byte(b.String()))})
}

func (m *Manager) unsubscribeScreen(sid string) {
	m.mu.Lock()
	s := m.sessions[sid]
	var client *terminal.Client
	if s != nil {
		client, s.client = s.client, nil
		s.lastCols, s.lastRows = 0, 0
	}
	m.mu.Unlock()
	if s != nil {
		// The last viewer left — never strand the pane in copy-mode, or the next
		// viewer (and the driver's live sampling) would open into a frozen scroll.
		s.term.ExitCopyMode()
	}
	if client != nil {
		client.Close()
	}
}

func (m *Manager) closeSession(sid string) {
	m.mu.Lock()
	s := m.sessions[sid]
	delete(m.sessions, sid)
	m.mu.Unlock()
	m.teardown(s)
	m.publishState()
}

// closeViewerClients detaches every live viewer pty (s.client) WITHOUT touching the
// tmux sessions/tasks — used before a self-update exec so no viewer client orphans.
func (m *Manager) closeViewerClients() {
	m.mu.Lock()
	clients := make([]*terminal.Client, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.client != nil {
			clients = append(clients, s.client)
			s.client, s.lastCols, s.lastRows = nil, 0, 0
		}
	}
	m.mu.Unlock()
	for _, c := range clients {
		c.Close()
	}
}

func (m *Manager) closeAll() {
	m.mu.Lock()
	all := m.sessions
	m.sessions = map[string]*sess{}
	m.mu.Unlock()
	for _, s := range all {
		m.teardown(s)
	}
}

func (m *Manager) teardown(s *sess) {
	if s == nil {
		return
	}
	if s.client != nil {
		s.client.Close()
	}
	s.cancel()
	_ = s.term.Close()
}

// execMaxOut caps each of stdout/stderr so a runaway command can't exhaust memory.
const execMaxOut = 1 << 20 // 1 MiB per stream

// runExec runs a one-shot command on this machine and returns stdout/stderr/exit
// to the server. This is a generic device capability, independent of screens —
// for setup, health checks, and other fire-and-forget device-side work. The
// caller may bound it (Timeout), feed it input (Stdin) and cancel it mid-run
// (an exec.cancel with the same ID); output beyond execMaxOut is dropped.
func (m *Manager) runExec(msg protocol.Msg) {
	if len(msg.Command) == 0 {
		_ = m.conn.Send(protocol.Msg{T: "exec.result", ID: msg.ID, Stderr: "empty command", Exit: -1})
		return
	}
	timeout := time.Duration(msg.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(m.ctx, timeout)
	defer cancel()
	if msg.ID != "" { // register so an exec.cancel can stop us
		m.execMu.Lock()
		m.execs[msg.ID] = cancel
		m.execMu.Unlock()
		defer func() {
			m.execMu.Lock()
			delete(m.execs, msg.ID)
			m.execMu.Unlock()
		}()
	}

	cmd := exec.CommandContext(ctx, msg.Command[0], msg.Command[1:]...)
	if msg.Cwd != "" {
		cmd.Dir = msg.Cwd
	}
	env := os.Environ()
	for k, v := range msg.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	if msg.Stdin != "" {
		cmd.Stdin = strings.NewReader(msg.Stdin)
	}
	stdout := &cappedBuffer{limit: execMaxOut}
	stderr := &cappedBuffer{limit: execMaxOut}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	exit := 0
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			exit = -1
			if stderr.Len() == 0 {
				stderr.buf.WriteString(err.Error())
			}
		}
	}
	_ = m.conn.Send(protocol.Msg{T: "exec.result", ID: msg.ID,
		Stdout: stdout.String(), Stderr: stderr.String(), Exit: exit,
		Truncated: stdout.truncated || stderr.truncated})
}

func (m *Manager) cancelExec(id string) {
	m.execMu.Lock()
	cancel := m.execs[id]
	m.execMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// fileWriteMode is the permission every file.write creates or overwrites a file with: owner
// read/write only. Every caller of this RPC so far (CA cert, proxy credentials, settings.json) is
// writing something either secret or meant for exactly one user's Claude Code to read — there is no
// current use case for anything more permissive, so this is not a caller-supplied field.
const fileWriteMode = 0o600

// fileWrite writes msg.Data (base64) to msg.Path, creating parent directories as needed and
// replacing any existing file atomically (write to a sibling temp file, then rename) — the same
// mkdir+write-temp+rename shape the bash script this replaces used, done with os calls instead of a
// shell so it works identically on every platform this connector targets.
func (m *Manager) fileWrite(msg protocol.Msg) {
	respond := func(errText string) {
		_ = m.conn.Send(protocol.Msg{T: "file.write.result", ID: msg.ID, Error: errText})
	}
	if msg.Path == "" {
		respond("file.write: empty path")
		return
	}
	data, err := base64.StdEncoding.DecodeString(msg.Data)
	if err != nil {
		respond(fmt.Sprintf("file.write: bad base64: %v", err))
		return
	}
	if err := os.MkdirAll(filepath.Dir(msg.Path), 0o700); err != nil {
		respond(fmt.Sprintf("file.write: mkdir: %v", err))
		return
	}
	tmp := msg.Path + ".tmp"
	if err := os.WriteFile(tmp, data, fileWriteMode); err != nil {
		respond(fmt.Sprintf("file.write: %v", err))
		return
	}
	if err := os.Rename(tmp, msg.Path); err != nil {
		_ = os.Remove(tmp)
		respond(fmt.Sprintf("file.write: rename: %v", err))
		return
	}
	respond("")
}

// fileRead answers with msg.Path's content (base64), or an empty, error-free result when the file
// does not exist — matching the `cat 'path' 2>/dev/null || true` this replaces, which callers here
// use for a read-modify-write merge over a file that may not have been created yet.
func (m *Manager) fileRead(msg protocol.Msg) {
	if msg.Path == "" {
		_ = m.conn.Send(protocol.Msg{T: "file.read.result", ID: msg.ID, Error: "file.read: empty path"})
		return
	}
	b, err := os.ReadFile(msg.Path)
	if err != nil {
		if os.IsNotExist(err) {
			_ = m.conn.Send(protocol.Msg{T: "file.read.result", ID: msg.ID})
			return
		}
		_ = m.conn.Send(protocol.Msg{T: "file.read.result", ID: msg.ID, Error: err.Error()})
		return
	}
	_ = m.conn.Send(protocol.Msg{T: "file.read.result", ID: msg.ID, Data: base64.StdEncoding.EncodeToString(b)})
}

// fileRemove deletes msg.Path. Removing a file that is already gone is success, not an error —
// matching `rm -f`, the cleanup this replaces.
func (m *Manager) fileRemove(msg protocol.Msg) {
	errText := ""
	if msg.Path == "" {
		errText = "file.remove: empty path"
	} else if err := os.Remove(msg.Path); err != nil && !os.IsNotExist(err) {
		errText = err.Error()
	}
	_ = m.conn.Send(protocol.Msg{T: "file.remove.result", ID: msg.ID, Error: errText})
}

// homeDir answers with this machine's real home directory. os.UserHomeDir() alone is not enough:
// on Unix it is nothing more than a $HOME read, which is genuinely unset in some real launch
// contexts (an SSH-driven non-login `exec`, some service/unit configurations) even though the
// account plainly has a home directory — so it falls back to os/user.Current(), which resolves
// the real account record (getpwuid on Unix) rather than an environment variable. Needs no shell
// to exist on the machine at all either way.
func (m *Manager) homeDir(msg protocol.Msg) {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		_ = m.conn.Send(protocol.Msg{T: "homedir.result", ID: msg.ID, Path: home})
		return
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		_ = m.conn.Send(protocol.Msg{T: "homedir.result", ID: msg.ID, Path: u.HomeDir})
		return
	}
	_ = m.conn.Send(protocol.Msg{T: "homedir.result", ID: msg.ID, Error: "homedir: could not resolve a home directory"})
}

// cappedBuffer accumulates up to limit bytes and silently drops the rest, so a
// runaway command's output can never blow up memory. It always reports a full
// write, so the child process is never blocked by a full pipe.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
			c.truncated = true
		} else {
			c.buf.Write(p)
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return c.buf.String() }
func (c *cappedBuffer) Len() int       { return c.buf.Len() }

// busAdapter maps one screen's runtime.Bus onto sid-tagged protocol messages. The runtime knows
// nothing about the control plane, and this knows nothing about how the messages travel — which is
// what lets the same applet run over a resident WebSocket here and over one-shot HTTP elsewhere.
type busAdapter struct {
	conn protocol.Transport
	sid  string
}

func (b *busAdapter) PushVar(name string, value any) {
	_ = b.conn.Send(protocol.Msg{T: "var.push", Sid: b.sid, Name: name, Value: value})
}

func (b *busAdapter) CallServer(id, name string, args []any) {
	_ = b.conn.Send(protocol.Msg{T: "rpc.call", Sid: b.sid, ID: id, Name: name, Args: args})
}

func (b *busAdapter) ReplyServer(id string, result any, errStr string) {
	_ = b.conn.Send(protocol.Msg{T: "rpc.result", Sid: b.sid, ID: id, Value: result, Error: errStr})
}

// SetInbound is unused: the host routes inbound messages to the runtime directly
// (see onMsg), so the adapter needs no reference back.
func (b *busAdapter) SetInbound(runtime.Inbound) {}

// screenTmpDir is a scratch directory belonging to this user, made once per screen so one agent
// cannot read what another left behind. It sits beside the runtime dir rather than in /tmp — see
// brand.RuntimePath for why that matters.
func screenTmpDir(sid string) (string, error) {
	dir := filepath.Join(brand.Current.RuntimePath(), "tmp", sid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
