// Tests for the screen manager against a real tmux server of the test's own (HOME, TMPDIR and
// XDG_RUNTIME_DIR point into a temporary directory), with a transport that records what the
// manager sends. They skip when no tmux is installed.
package screen

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/micro-teams/micro-connector/cli/protocol"
	"github.com/micro-teams/micro-connector/cli/terminal"
)

type recorder struct {
	mu   sync.Mutex
	msgs []protocol.Msg
}

func (r *recorder) Run(ctx context.Context, _ func(protocol.Msg)) error { <-ctx.Done(); return nil }

func (r *recorder) Send(m protocol.Msg) error {
	r.mu.Lock()
	r.msgs = append(r.msgs, m)
	r.mu.Unlock()
	return nil
}

func (r *recorder) all() []protocol.Msg {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]protocol.Msg(nil), r.msgs...)
}

// find returns the last message matching ok.
func (r *recorder) find(ok func(protocol.Msg) bool) *protocol.Msg {
	all := r.all()
	for i := len(all) - 1; i >= 0; i-- {
		if ok(all[i]) {
			return &all[i]
		}
	}
	return nil
}

// data is everything sent to one viewer as screen.data.
func (r *recorder) data(viewer string) string {
	var b strings.Builder
	for _, m := range r.all() {
		if m.T == "screen.data" && m.Viewer == viewer {
			d, _ := base64.StdEncoding.DecodeString(m.Data)
			b.Write(d)
		}
	}
	return b.String()
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

type fixture struct {
	t    *testing.T
	tm   *terminal.Manager
	m    *Manager
	conn *recorder
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dir, err := os.MkdirTemp("", "mc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMPDIR", dir)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("HOME", dir)
	tm, err := terminal.NewManager()
	if err != nil {
		t.Skipf("no tmux: %v", err)
	}
	t.Cleanup(func() { tm.KillServer() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conn := &recorder{}
	return &fixture{t: t, tm: tm, m: NewManager(ctx, conn, tm), conn: conn}
}

func (f *fixture) open(sid string, argv ...string) {
	f.t.Helper()
	f.m.Dispatch(protocol.Msg{T: "session.create", Sid: sid, Command: argv, Cols: 100, Rows: 30})
	eventually(f.t, "session.ready", func() bool {
		return f.conn.find(func(m protocol.Msg) bool { return m.T == "session.ready" && m.Sid == sid }) != nil
	})
}

func (f *fixture) input(sid, viewer, text string) {
	f.m.Dispatch(protocol.Msg{T: "screen.input", Sid: sid, Viewer: viewer, Data: base64.StdEncoding.EncodeToString([]byte(text))})
}

func (f *fixture) window(sid string) string {
	f.t.Helper()
	s := f.m.screen(sid)
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("%dx%d", s.cols, s.rows)
}

func TestViewersShareTheSmallestSize(t *testing.T) {
	f := setup(t)
	f.open("s1", "sh", "-c", "echo program-output; sleep 60")
	f.m.Dispatch(protocol.Msg{T: "screen.subscribe", Sid: "s1", Viewer: "a", Cols: 100, Rows: 30})
	f.m.Dispatch(protocol.Msg{T: "screen.subscribe", Sid: "s1", Viewer: "b", Cols: 80, Rows: 40})
	for _, v := range []string{"a", "b"} {
		eventually(t, "a full paint for "+v, func() bool { return strings.Contains(f.conn.data(v), "program-output") })
		eventually(t, "the shared size for "+v, func() bool {
			s := f.conn.find(func(m protocol.Msg) bool { return m.T == "screen.size" && m.Viewer == v })
			return s != nil && s.Cols == 80 && s.Rows == 30
		})
	}
	f.m.Dispatch(protocol.Msg{T: "screen.unsubscribe", Sid: "s1", Viewer: "b"})
	eventually(t, "the window to grow back", func() bool { return f.window("s1") == "100x30" })
	f.m.Dispatch(protocol.Msg{T: "screen.resize", Sid: "s1", Viewer: "a", Cols: 60, Rows: 20})
	eventually(t, "a resize to reach the window", func() bool { return f.window("s1") == "60x20" })
}

func TestTheLeaseKeepsOneTypistAtATime(t *testing.T) {
	f := setup(t)
	out := t.TempDir() + "/heard"
	f.open("s1", "sh", "-c", "stty raw -echo; cat > "+out)
	for _, v := range []string{"a", "b"} {
		f.m.Dispatch(protocol.Msg{T: "screen.subscribe", Sid: "s1", Viewer: v, Cols: 80, Rows: 24})
	}
	time.Sleep(500 * time.Millisecond)
	f.input("s1", "a", "from-a ")
	f.input("s1", "b", "from-b ")
	eventually(t, "b to be told a holds the lease", func() bool {
		return f.conn.find(func(m protocol.Msg) bool { return m.T == "screen.lease.state" && m.Viewer == "b" && m.Holder == "a" }) != nil
	})
	s := f.m.screen("s1")
	if err := (&driverTerm{m: f.m, s: s}).Write([]byte("from-driver ")); err == nil {
		t.Fatal("the applet typed while a person held the lease")
	}
	time.Sleep(leaseTTL + 500*time.Millisecond)
	if last := f.conn.find(func(m protocol.Msg) bool { return m.T == "screen.lease.state" && m.Viewer == "b" }); last.Holder != "" {
		t.Fatalf("the lease did not lapse: held by %q", last.Holder)
	}
	f.input("s1", "b", "then-b")
	eventually(t, "the program to hear a then b", func() bool {
		got, _ := os.ReadFile(out)
		return string(got) == "from-a then-b"
	})
}

func TestAReadoptKeepsTheAppletUnlessItChanged(t *testing.T) {
	f := setup(t)
	src := `connector.own('loads', 1)`
	f.m.Dispatch(protocol.Msg{T: "session.create", Sid: "s1", Command: []string{"sleep", "60"}, Cols: 80, Rows: 24, Source: src})
	pushes := func() int {
		n := 0
		for _, m := range f.conn.all() {
			if m.T == "var.push" && m.Name == "loads" {
				n++
			}
		}
		return n
	}
	eventually(t, "the applet to load", func() bool { return pushes() == 1 })
	f.m.Dispatch(protocol.Msg{T: "session.create", Sid: "s1", Adopt: true, Source: src})
	time.Sleep(500 * time.Millisecond)
	if pushes() != 1 {
		t.Fatal("the same applet was loaded again")
	}
	f.m.Dispatch(protocol.Msg{T: "session.create", Sid: "s1", Adopt: true, Source: src + ";"})
	eventually(t, "a changed applet to load", func() bool { return pushes() == 2 })
}

func TestDetachKeepsProgramsAndAMissingOneIsReported(t *testing.T) {
	f := setup(t)
	f.open("s1", "sleep", "60")
	f.m.Detach()
	if ok, err := f.tm.HasSession("s1"); !ok || err != nil {
		t.Fatalf("Detach ended the program: %v %v", ok, err)
	}
	f.m.Dispatch(protocol.Msg{T: "session.create", Sid: "s2", Adopt: true})
	eventually(t, "the missing screen to be reported", func() bool {
		return f.conn.find(func(m protocol.Msg) bool {
			return m.T == "session.error" && m.Sid == "s2" && strings.Contains(m.Error, "no longer running")
		}) != nil
	})
	f.m.Dispatch(protocol.Msg{T: "screen.subscribe", Sid: "s3", Viewer: "a", Cols: 80, Rows: 24})
	eventually(t, "a viewer of an unknown screen to be told", func() bool {
		return f.conn.find(func(m protocol.Msg) bool { return m.T == "screen.error" && m.Sid == "s3" && m.Viewer == "a" }) != nil
	})
}

func TestCaptureReturnsTheText(t *testing.T) {
	f := setup(t)
	f.open("s1", "sh", "-c", "seq 1 300; sleep 60")
	f.m.Dispatch(protocol.Msg{T: "screen.subscribe", Sid: "s1", Viewer: "a", Cols: 80, Rows: 24})
	eventually(t, "a capture with history", func() bool {
		f.m.Dispatch(protocol.Msg{T: "screen.capture", Sid: "s1", Viewer: "a", ID: "c1"})
		time.Sleep(200 * time.Millisecond)
		r := f.conn.find(func(m protocol.Msg) bool { return m.T == "screen.capture.result" && m.ID == "c1" })
		if r == nil {
			return false
		}
		d, _ := base64.StdEncoding.DecodeString(r.Data)
		return strings.HasPrefix(string(d), "1\n2\n") && strings.Contains(string(d), "\n300\n")
	})
}

func TestEachScreenGetsAPrivateTempDir(t *testing.T) {
	f := setup(t)
	out := t.TempDir() + "/tmpdir"
	f.open("s1", "sh", "-c", "echo $TMPDIR > "+out+"; sleep 60")
	eventually(t, "the program to report", func() bool {
		got, _ := os.ReadFile(out)
		dir := strings.TrimSpace(string(got))
		st, err := os.Stat(dir)
		return err == nil && st.Mode().Perm() == 0o700 && strings.HasSuffix(dir, "/tmp/s1")
	})
}
