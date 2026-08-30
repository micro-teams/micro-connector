package screen

import (
	"context"
	"sync"
	"testing"

	"github.com/micro-teams/micro-connector/cli/protocol"
)

type recorder struct {
	mu   sync.Mutex
	sent []protocol.Msg
}

func (r *recorder) Run(ctx context.Context, onMsg func(protocol.Msg)) error { <-ctx.Done(); return nil }

func (r *recorder) Send(m protocol.Msg) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, m)
	return nil
}

func (r *recorder) all() []protocol.Msg {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]protocol.Msg(nil), r.sent...)
}

// Asking to watch a screen this machine is not hosting must be ANSWERED, not dropped.
//
// The report: "microteams and the CLI both say connected, but the screen cannot actually be
// opened." The link was fine; the sessions were not. A stop kills the tmux server (`link
// disconnect` does) and a reboot takes it with /tmp, so after reconnecting the server still holds
// records for sessions that no longer exist. Returning in silence leaves it holding them, and
// leaves a person watching a terminal that never opens.
func TestSubscribingToAScreenWeDoNotHostSaysSo(t *testing.T) {
	conn := &recorder{}
	m := &Manager{conn: conn, sessions: map[string]*sess{}}

	m.subscribeScreen("s-not-here", 80, 24)

	sent := conn.all()
	if len(sent) == 0 {
		t.Fatal("said nothing at all — the viewer waits forever and the server keeps believing")
	}
	got := sent[0]
	if got.T != "session.error" {
		t.Fatalf("answered %q, want session.error so the server marks it dead and rebuilds", got.T)
	}
	if got.Sid != "s-not-here" {
		t.Fatalf("answered about %q, not the screen that was asked for", got.Sid)
	}
	if got.Error == "" {
		t.Fatal("an error with nothing in it tells nobody why")
	}
}
