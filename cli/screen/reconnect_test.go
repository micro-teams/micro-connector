package screen

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/micro-teams/micro-connector/cli/protocol"
	"github.com/micro-teams/micro-connector/cli/terminal"
)

// What a person did, and what happened.
//
// They set some permissions by hand, then ran `link disconnect` and `link connect` to clear an
// interruption. Both the app and the CLI said connected — and the terminal could not be opened.
//
// Stopping the connector kills the tmux server on purpose ("a stop is a stop"), and a reboot takes
// it too. What comes back is the link, not the sessions, while the control plane still holds
// records for every screen it believes is live. Then somebody presses open, the connector is asked
// to attach a session it does not have, and — before this test — said nothing at all. The link is
// healthy, the status is green, and a person watches a terminal that never opens.
//
// This drives the real thing: a real tmux server, a session made through the manager, the server
// killed behind its back the way a stop or a reboot kills it, and then the open.
func TestOpeningAScreenAfterTmuxDiedIsAnswered(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux available")
	}
	// Every input the runtime path reads, pointed at this test's own directory — see the same
	// reasoning in terminal's isolated(): these tests kill tmux servers, and the suite once killed
	// the live one.
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

	m.Dispatch(protocol.Msg{T: "session.create", Sid: "s1", Command: []string{"sh", "-c", "sleep 60"}})
	deadline := time.Now().Add(5 * time.Second)
	for !tm.HasSession("s1") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !tm.HasSession("s1") {
		t.Fatal("the session never started, so this test cannot say anything about losing it")
	}

	// The stop, the disconnect, the reboot: the tmux server goes, the connector is not told.
	tm.KillServer()

	before := len(conn.all())
	m.Dispatch(protocol.Msg{T: "screen.subscribe", Sid: "s1", Cols: 80, Rows: 24})

	var answer *protocol.Msg
	for _, msg := range conn.all()[before:] {
		if msg.T == "session.error" && msg.Sid == "s1" {
			answer = &msg
			break
		}
	}
	if answer == nil {
		var said []string
		for _, msg := range conn.all()[before:] {
			said = append(said, msg.T)
		}
		t.Fatalf("opening a screen whose tmux is gone was answered with %v — "+
			"the server goes on believing it is live and the viewer waits forever", said)
	}
	if !strings.Contains(answer.Error, "screen") {
		t.Fatalf("the answer does not say what is wrong: %q", answer.Error)
	}
}
