package screen

import (
	"context"
	"encoding/base64"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/micro-teams/micro-connector/cli/protocol"
	"github.com/micro-teams/micro-connector/cli/terminal"
)

// TestASecondViewerGetsASnapshot is T-091: a screen that already has one viewer attached silently
// gave a second viewer nothing at all — no clear, no repaint, nothing — because subscribeScreen
// bailed out the moment s.client was already set. The second viewer's local terminal buffer then
// stayed blank/wrong until enough scattered future deltas happened to coincidentally paint over it,
// or an unrelated resize forced a real repaint. Confirmed against a real packet capture and a live
// tmux session; see the header comment on ViewerPump.go's backend counterpart for the same gap.
//
// This drives real tmux end to end: spawn a session, attach a first real client (as the hub's
// screen.subscribe for the FIRST viewer does), write recognizable content into the pane, then send a
// second screen.subscribe (as the backend now also sends for a later viewer — see the corresponding
// MachineHub.attachViewer fix) and assert a screen.data snapshot containing that content actually
// goes out, where before this fix nothing did.
func TestASecondViewerGetsASnapshot(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux available")
	}
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("HOME", dir)

	tm, err := terminal.NewManager()
	if err != nil {
		t.Skipf("no terminal manager here: %v", err)
	}
	defer tm.KillServer()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := &recorder{}
	m := NewManager(ctx, conn, tm)

	const marker = "SECOND-VIEWER-MARKER-91"
	m.Dispatch(protocol.Msg{T: "session.create", Sid: "s1",
		Command: []string{"sh", "-c", "echo " + marker + "; sleep 30"}})
	deadline := time.Now().Add(5 * time.Second)
	for !tm.HasSession("s1") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !tm.HasSession("s1") {
		t.Fatal("the session never started")
	}

	// The FIRST viewer: a real screen.subscribe, which does a real tmux attach.
	m.Dispatch(protocol.Msg{T: "screen.subscribe", Sid: "s1", Cols: 80, Rows: 24})

	// Wait for the marker to actually land in the pane so the snapshot has something
	// recognizable to prove it captured live content, not stale/empty output.
	s1 := m.session("s1")
	if s1 == nil {
		t.Fatal("session s1 vanished from the manager right after creating it")
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		out, capErr := s1.term.CaptureANSI()
		if capErr == nil && strings.Contains(out, marker) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("marker never appeared on the pane; last capture: %q (err=%v)", out, capErr)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Let the first attach's own pty stream settle: it can still be delivering trailing chunks of
	// its own real repaint asynchronously right after the marker lands on the pane, and those must
	// not be mistaken for something the second subscribe caused.
	time.Sleep(300 * time.Millisecond)
	before := len(conn.all())

	// The SECOND viewer joins the same, already-attached screen.
	m.Dispatch(protocol.Msg{T: "screen.subscribe", Sid: "s1", Cols: 80, Rows: 24})

	// Give the async attach/whatever a brief moment; sendSyntheticSnapshot itself is synchronous
	// within Dispatch, but keep this generous and deterministic rather than racy.
	var snapshot *protocol.Msg
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, msg := range conn.all()[before:] {
			if msg.T == "screen.data" {
				mm := msg
				snapshot = &mm
			}
		}
		if snapshot != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if snapshot == nil {
		var said []string
		for _, msg := range conn.all()[before:] {
			said = append(said, msg.T)
		}
		t.Fatalf("a second viewer joining an already-attached screen got %v — before this fix it got "+
			"nothing at all and stayed blank until an unrelated change happened to paint over it", said)
	}

	raw, err := base64.StdEncoding.DecodeString(snapshot.Data)
	if err != nil {
		t.Fatalf("screen.data payload did not decode: %v", err)
	}
	if !strings.Contains(string(raw), marker) {
		t.Fatalf("the synthetic snapshot does not contain the pane's actual content (marker %q); got:\n%q",
			marker, raw)
	}
	if !strings.Contains(string(raw), "\x1b[H\x1b[J") {
		t.Error("the synthetic snapshot does not open with a clear+home, unlike a real attach")
	}
}
