// Tests for the terminal package against a real tmux. Every test runs its own tmux server under a
// temporary HOME, TMPDIR and XDG_RUNTIME_DIR (see isolated), so the suite never reaches a server it
// did not start; tests skip when no tmux is installed.
package terminal

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/micro-teams/micro-connector/cli/brand"
)

func needTmux(t *testing.T) {
	t.Helper()
	if _, err := findTmux(); err != nil {
		t.Skip("no tmux available")
	}
}

// shortTempDir is a temporary directory whose path does not contain the test's name: the tmux
// socket lives inside it, and a unix socket path is limited to about 104 bytes.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// isolated returns a Manager whose tmux server belongs to this test alone, and checks that its
// socket really is inside the test's directory before anything can kill it.
func isolated(t *testing.T) *Manager {
	t.Helper()
	needTmux(t)
	dir := shortTempDir(t)
	t.Setenv("TMPDIR", dir)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("HOME", dir)
	m, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if !strings.HasPrefix(m.sock, dir) {
		t.Fatalf("the test's tmux server is at %q, outside %q", m.sock, dir)
	}
	t.Cleanup(func() { m.KillServer() })
	return m
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func format(t *testing.T, m *Manager, target, f string) string {
	t.Helper()
	out, err := m.output("display-message", "-p", "-t", target, f)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

func TestHasSessionAdoptAndClose(t *testing.T) {
	m := isolated(t)
	if ok, err := m.HasSession("nope"); ok || err != nil {
		t.Fatalf("HasSession with no server = %v, %v; want false, nil", ok, err)
	}
	s, err := m.Spawn("t1", []string{"sleep", "60"}, nil, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := m.HasSession("t1"); !ok || err != nil {
		t.Fatalf("HasSession of a live session = %v, %v", ok, err)
	}
	// A session whose name only starts with another's is not that session.
	if ok, _ := m.HasSession("t"); ok {
		t.Fatal("HasSession matched a prefix")
	}
	if n, err := m.LiveSessions(); n != 1 || err != nil {
		t.Fatalf("LiveSessions = %d, %v", n, err)
	}
	if err := m.Adopt("t1").Close(); err != nil {
		t.Fatal(err)
	}
	if ok, err := m.HasSession("t1"); ok || err != nil {
		t.Fatalf("HasSession after Close = %v, %v", ok, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("closing a session that is already gone: %v", err)
	}
}

func TestConfigReachesAServerThatIsAlreadyRunning(t *testing.T) {
	m := isolated(t)
	if out, err := exec.Command(m.bin, "-S", m.sock, "-f", "/dev/null", "new-session", "-d", "-s", "old", "sleep 60").CombinedOutput(); err != nil {
		t.Fatalf("start a server with no configuration: %v: %s", err, out)
	}
	again, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	if got := format(t, again, "=old:", "#{status} #{mouse} #{prefix}"); got != "off 1 None" {
		t.Fatalf("options on the running server = %q", got)
	}
}

func TestTextIncludesHistory(t *testing.T) {
	m := isolated(t)
	s, err := m.Spawn("h", []string{"sh", "-c", "seq 1 100; sleep 60"}, nil, 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the output", func() bool {
		txt, err := s.Text(1000)
		f := strings.Fields(txt)
		return err == nil && len(f) >= 100 && f[0] == "1" && f[99] == "100"
	})
}

func TestWriteLeavesCopyModeAndKeepsLongMessagesWhole(t *testing.T) {
	m := isolated(t)
	out := filepath.Join(t.TempDir(), "heard.txt")
	s, err := m.Spawn("w", []string{"sh", "-c", "stty raw -echo; cat > " + out}, nil, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if err := m.run("copy-mode", "-t", s.target()); err != nil {
		t.Fatal(err)
	}
	// Past tmux's command length limit, and half of it multi-byte, which a naive split corrupts.
	body := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 250) +
		strings.Repeat("这是一条很长的中文消息，用来确认分片不会把一个字劈成两半。", 250)
	if err := s.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the whole message", func() bool {
		got, _ := os.ReadFile(out)
		return len(got) >= len(body)
	})
	if got, _ := os.ReadFile(out); string(got[:len(body)]) != body {
		t.Fatal("the program received different bytes")
	}
	if mode := format(t, m, s.target(), "#{pane_in_mode}"); mode != "0" {
		t.Fatalf("pane_in_mode = %s after Write", mode)
	}
}

func TestChunkEnd(t *testing.T) {
	body := bytes.Repeat([]byte("中"), 10)
	for max := 3; max <= len(body); max++ {
		if n := chunkEnd(body, max); n%3 != 0 || n > max || n == 0 {
			t.Errorf("chunkEnd(max=%d) = %d", max, n)
		}
	}
	if got := chunkEnd([]byte("ab"), 8); got != 2 {
		t.Errorf("short input: %d", got)
	}
	if got := chunkEnd([]byte{0x80, 0x80, 0x80, 0x80}, 2); got != 2 {
		t.Errorf("invalid UTF-8: %d", got)
	}
}

// collector gathers a client's output.
type collector struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *collector) add(b []byte) { c.mu.Lock(); c.buf.Write(b); c.mu.Unlock() }
func (c *collector) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}
func (c *collector) Len() int { c.mu.Lock(); defer c.mu.Unlock(); return c.buf.Len() }

func TestEachClientIsPaintedWholeAtTheWindowSize(t *testing.T) {
	m := isolated(t)
	s, err := m.Spawn("a", []string{"sh", "-c", "echo hello-from-the-program; sleep 60"}, nil, 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	var first, second collector
	c1, err := s.Attach(100, 30, Window, first.add, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	eventually(t, "the first client's paint", func() bool { return strings.Contains(first.String(), "hello-from-the-program") })

	if err := s.Resize(60, 20); err != nil {
		t.Fatal(err)
	}
	if err := c1.Resize(60, 20); err != nil {
		t.Fatal(err)
	}
	c2, err := s.Attach(60, 20, Window, second.add, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	eventually(t, "the second client's paint", func() bool { return strings.Contains(second.String(), "hello-from-the-program") })
	if got := format(t, m, s.target(), "#{window_width}x#{window_height}"); got != "60x20" {
		t.Fatalf("window = %s, want 60x20 whatever the clients' sizes", got)
	}
}

func TestAClientThatIsNotAcknowledgedStopsBeingRead(t *testing.T) {
	m := isolated(t)
	s, err := m.Spawn("f", []string{"sh", "-c", "sleep 1; while true; do seq 1 1000; done"}, nil, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	var got collector
	c, err := s.Attach(80, 24, Window, got.add, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	eventually(t, "the window to fill", func() bool { return got.Len() >= Window })
	time.Sleep(time.Second)
	if n := got.Len(); n > Window+32<<10 {
		t.Fatalf("read %d bytes with nothing acknowledged; the limit is %d plus one read", n, Window)
	}
	before := got.Len()
	c.Ack(before)
	eventually(t, "reading to resume", func() bool { return got.Len() > before })
}

func TestAnEndedClientIsReported(t *testing.T) {
	m := isolated(t)
	s, err := m.Spawn("e", []string{"sleep", "60"}, nil, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	c, err := s.Attach(80, 24, Window, func([]byte) {}, func(err error) { ended <- err })
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	time.Sleep(300 * time.Millisecond)
	if err := m.run("detach-client", "-s", "=e"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("a client that ended was not reported")
	}
}

// An update in place must come back to the socket a server is still answering on, wherever an
// older build put it; going elsewhere would orphan every screen it hosts.
func TestAServerAtAnOlderLocationIsJoined(t *testing.T) {
	needTmux(t)
	for name, old := range map[string]func() string{
		"temp dir":        brand.Current.LegacyRuntimePath,
		"XDG runtime dir": brand.Current.XDGRuntimePath,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TMPDIR", shortTempDir(t))
			t.Setenv("XDG_RUNTIME_DIR", shortTempDir(t))
			t.Setenv("HOME", shortTempDir(t))
			if err := os.MkdirAll(old(), 0o700); err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(old(), "t.sock")
			bin, _ := findTmux()
			if out, err := exec.Command(bin, "-S", want, "new-session", "-d", "sleep 60").CombinedOutput(); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			defer exec.Command(bin, "-S", want, "kill-server").Run()
			m, err := NewManager()
			if err != nil {
				t.Fatal(err)
			}
			if m.sock != want {
				t.Fatalf("went to %q instead of %q", m.sock, want)
			}
		})
	}
}

// The service and a person's login shell meet at one socket, and a stray directory or a dead
// socket file at an older location does not pull the shell away.
func TestAShellFindsTheSocketTheServiceUses(t *testing.T) {
	needTmux(t)
	t.Setenv("TMPDIR", shortTempDir(t))
	t.Setenv("HOME", shortTempDir(t))
	t.Setenv("XDG_RUNTIME_DIR", "")
	service, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", shortTempDir(t))
	if err := os.MkdirAll(brand.Current.XDGRuntimePath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brand.Current.XDGRuntimePath(), "t.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	shell, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	if shell.sock != service.sock {
		t.Fatalf("a login shell looks at %q, the service hosts at %q", shell.sock, service.sock)
	}
}

func TestARuntimeDirThatCannotBeMadeIsAnError(t *testing.T) {
	needTmux(t)
	t.Setenv("TMPDIR", shortTempDir(t))
	t.Setenv("XDG_RUNTIME_DIR", "")
	home := shortTempDir(t)
	if err := os.MkdirAll(filepath.Join(home, ".local"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".local", "state"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if _, err := NewManager(); err == nil {
		t.Fatal("NewManager succeeded with no usable runtime dir")
	}
}
