// Package terminal hosts programs in a private tmux server. It knows nothing about what runs inside:
// it spawns an argv, reads and writes the screen, sizes the window, and attaches a real tmux client
// for each viewer.
//
// One tmux server per user and brand holds every screen, on a socket under the brand's runtime
// path. The server outlives the process that started it, so a connector that restarts or replaces
// itself finds its screens again by name. Its configuration makes tmux an invisible substrate: no
// status line, no prefix key, the mouse on (so a viewer's wheel scrolls tmux history or reaches a
// program that asked for the mouse), and the clipboard passed through as OSC 52. Every window is
// sized only by the connector: Spawn and Resize pin it with resize-window, which makes the window's
// own window-size manual, so a client attaching at another size cannot change it.
//
// Manager
//
//	NewManager() (*Manager, error)
//	    Finds tmux (the brand's TMUX variable, then <config>/<brand>/bin/tmux, then PATH) and
//	    applies the configuration to the server, starting it if needed. ErrUnsupported on Windows.
//	(*Manager).Spawn(name, argv, env []string, cols, rows int) (*Session, error)
//	    Starts argv in a new session of the given size; env holds KEY=VALUE pairs.
//	(*Manager).Adopt(name) *Session
//	    Wraps a session that already exists, e.g. after the connector restarted.
//	(*Manager).HasSession(name) (bool, error)
//	    false only when tmux says the session or the server does not exist; any other failure is
//	    an error, because a busy tmux is not a dead screen.
//	(*Manager).LiveSessions() (int, error)
//	    How many sessions the server has; 0 when no server runs.
//	(*Manager).KillServer() error
//	    Ends every session. For uninstalling and for tests, never for stopping a connector.
//
// Session
//
//	Name() string; Snapshot() string (the last polled screen, plain text)
//	OnChange(fn)  starts a 400 ms poller and calls fn on every change, and every 1.2 s regardless,
//	              so a driver can act on a screen that stays the same.
//	OnGone(fn)    calls fn once when tmux confirms the session no longer exists.
//	Write(p) error  types p into the program, leaving copy-mode first; long input is chunked.
//	LeaveMode() error  leaves copy-mode (a viewer scrolled back) so the live screen shows again.
//	Resize(cols, rows) error  sets the window size; every client must be sized to match.
//	Text(history int) (string, error)  the screen as text, wrapped lines joined, with up to
//	              history lines of scrollback above it.
//	Attach(cols, rows, window, onData, onEnd) (*Client, error)  see client.go.
//	Stop()        stops polling and leaves the program running.
//	Close() error stops polling and kills the session.
//
// Example:
//
//	tm, err := terminal.NewManager()
//	s, err := tm.Spawn("s1", []string{"bash"}, nil, 120, 32)
//	s.OnChange(func() { fmt.Println(s.Snapshot()) })
//	err = s.Write([]byte("echo hi\r"))
package terminal

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/micro-teams/micro-connector/cli/brand"
)

// ErrUnsupported means this platform cannot host a screen at all: the pty package Attach relies on
// has no Windows implementation, so no tmux build would help there.
var ErrUnsupported = errors.New("terminal: screens are not supported on this platform")

// config is the server's configuration. It is applied on every NewManager, not only when the
// server starts, because a server that outlived an older connector must take the current one.
const config = `set -g remain-on-exit on
set -g status off
set -g prefix None
set -g prefix2 None
set -g mouse on
set -g escape-time 0
set -g history-limit 10000
set -g window-size smallest
set -g set-clipboard on
set -as terminal-features ',xterm-256color:clipboard'
`

// Manager owns the private tmux server.
type Manager struct {
	bin  string
	sock string
	conf string
}

func findTmux() (string, error) {
	if runtime.GOOS == "windows" {
		return "", ErrUnsupported
	}
	if p := brand.Current.Getenv("TMUX"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("terminal: %s points at %q: %w", brand.Current.Env("TMUX"), p, err)
		}
		return p, nil
	}
	if base, err := os.UserConfigDir(); err == nil {
		p := filepath.Join(base, brand.Current.ConfigDir, "bin", "tmux")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath("tmux"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("terminal: no tmux found (install one, or place a private copy at <config>/%s/bin/tmux)", brand.Current.ConfigDir)
}

func NewManager() (*Manager, error) {
	bin, err := findTmux()
	if err != nil {
		return nil, err
	}
	dir := brand.Current.RuntimePath()
	// A server still answering at a location an older connector used keeps hosting its screens;
	// moving to the current path would orphan them, so this process joins it there instead.
	for _, old := range []string{brand.Current.XDGRuntimePath(), brand.Current.LegacyRuntimePath()} {
		if old != "" && old != dir && answers(filepath.Join(old, "t.sock")) {
			dir = old
			break
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("terminal: runtime dir: %w", err)
	}
	m := &Manager{bin: bin, sock: filepath.Join(dir, "t.sock"), conf: filepath.Join(dir, "tmux.conf")}
	if err := os.WriteFile(m.conf, []byte(config), 0o600); err != nil {
		return nil, fmt.Errorf("terminal: write config: %w", err)
	}
	if answers(m.sock) {
		if err := m.run("source-file", m.conf); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// answers reports whether something accepts connections on the unix socket at path.
func answers(path string) bool {
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func (m *Manager) tmux(args ...string) *exec.Cmd {
	cmd := exec.Command(m.bin, append([]string{"-S", m.sock, "-f", m.conf}, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C.UTF-8", "LANG=C.UTF-8")
	return cmd
}

// output runs a tmux command and returns its stdout, or an error carrying tmux's own message.
func (m *Manager) output(args ...string) (string, error) {
	var out, stderr bytes.Buffer
	cmd := m.tmux(args...)
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", &tmuxError{args: args, err: err, msg: strings.TrimSpace(stderr.String())}
	}
	return out.String(), nil
}

func (m *Manager) run(args ...string) error {
	_, err := m.output(args...)
	return err
}

type tmuxError struct {
	args []string
	err  error
	msg  string
}

func (e *tmuxError) Error() string {
	return fmt.Sprintf("terminal: tmux %s: %v: %s", e.args[0], e.err, e.msg)
}

// absent reports whether err is tmux saying the session or the whole server does not exist.
func absent(err error) bool {
	var te *tmuxError
	if !errors.As(err, &te) {
		return false
	}
	return strings.HasPrefix(te.msg, "can't find session") || strings.HasPrefix(te.msg, "no server running") ||
		strings.HasPrefix(te.msg, "error connecting to")
}

func (m *Manager) KillServer() error {
	if err := m.run("kill-server"); err != nil && !absent(err) {
		return err
	}
	return nil
}

func (m *Manager) HasSession(name string) (bool, error) {
	err := m.run("has-session", "-t", "="+name)
	if err == nil {
		return true, nil
	}
	if absent(err) {
		return false, nil
	}
	return false, err
}

func (m *Manager) LiveSessions() (int, error) {
	out, err := m.output("list-sessions", "-F", "#{session_name}")
	if err != nil {
		if absent(err) {
			return 0, nil
		}
		return 0, err
	}
	return len(strings.Fields(out)), nil
}

func (m *Manager) Spawn(name string, argv, env []string, cols, rows int) (*Session, error) {
	if len(argv) == 0 {
		return nil, errors.New("terminal: spawn: no command")
	}
	if cols <= 0 || rows <= 0 {
		return nil, fmt.Errorf("terminal: spawn: size %dx%d", cols, rows)
	}
	launch := argv
	if len(env) > 0 {
		// Through env(1), so the variables reach the program and everything it starts.
		launch = append(append([]string{"env"}, env...), argv...)
	}
	args := append([]string{"new-session", "-d", "-s", name, "-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows)}, launch...)
	if err := m.run(args...); err != nil {
		return nil, err
	}
	s := m.Adopt(name)
	if err := s.Resize(cols, rows); err != nil {
		return nil, err
	}
	return s, nil
}

func (m *Manager) Adopt(name string) *Session {
	return &Session{m: m, name: name, stop: make(chan struct{})}
}

// Session is one hosted program.
type Session struct {
	m    *Manager
	name string

	mu       sync.Mutex
	last     string
	cbs      []func()
	gone     func()
	stop     chan struct{}
	stopOnce sync.Once
	start    sync.Once
}

func (s *Session) Name() string { return s.name }

func (s *Session) target() string { return "=" + s.name + ":" }

func (s *Session) Snapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *Session) OnGone(fn func()) {
	s.mu.Lock()
	s.gone = fn
	s.mu.Unlock()
}

func (s *Session) OnChange(fn func()) {
	s.mu.Lock()
	s.cbs = append(s.cbs, fn)
	s.mu.Unlock()
	s.start.Do(func() { go s.poll() })
}

// writeChunk bounds one send-keys: tmux rejects a command whose arguments are too long.
const writeChunk = 4096

func (s *Session) Write(p []byte) error {
	for len(p) > 0 {
		n := chunkEnd(p, writeChunk)
		// copy-mode -q leaves any mode first, so the bytes reach the program rather than a
		// scrolled-back view; both commands run in one tmux invocation.
		err := s.m.run("copy-mode", "-q", "-t", s.target(), ";", "send-keys", "-t", s.target(), "-l", "--", string(p[:n]))
		if err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

// chunkEnd returns how many bytes of p to send next: at most max, never splitting a UTF-8 sequence,
// because each chunk is a separate tmux argument and half a rune is not valid text.
func chunkEnd(p []byte, max int) int {
	if len(p) <= max {
		return len(p)
	}
	n := max
	for n > 0 && p[n]&0xC0 == 0x80 {
		n--
	}
	if n == 0 {
		return max
	}
	return n
}

func (s *Session) LeaveMode() error { return s.m.run("copy-mode", "-q", "-t", s.target()) }

func (s *Session) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("terminal: resize %s: size %dx%d", s.name, cols, rows)
	}
	return s.m.run("resize-window", "-t", s.target(), "-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows))
}

func (s *Session) Text(history int) (string, error) {
	return s.m.output("capture-pane", "-t", s.target(), "-p", "-J", "-S", strconv.Itoa(-history))
}

func (s *Session) poll() {
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for ticks := 1; ; ticks++ {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
		}
		cur, err := s.m.output("capture-pane", "-t", s.target(), "-p")
		if err != nil {
			if !absent(err) {
				fmt.Fprintf(os.Stderr, "%s: screen %s: %v\n", brand.Current.Name, s.name, err)
				continue
			}
			s.mu.Lock()
			fn := s.gone
			s.mu.Unlock()
			if fn != nil {
				fn()
			}
			return
		}
		s.mu.Lock()
		changed := cur != s.last
		s.last = cur
		cbs := append([]func(){}, s.cbs...)
		s.mu.Unlock()
		if changed || ticks%3 == 0 {
			for _, cb := range cbs {
				cb()
			}
		}
	}
}

func (s *Session) Stop() { s.stopOnce.Do(func() { close(s.stop) }) }

func (s *Session) Close() error {
	s.Stop()
	if err := s.m.run("kill-session", "-t", "="+s.name); err != nil && !absent(err) {
		return err
	}
	return nil
}
