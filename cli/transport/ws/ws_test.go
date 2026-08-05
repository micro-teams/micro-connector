// The seam that lets a product dial somewhere other than the one configured URL.
//
// Tested against a real WebSocket server rather than a fake dialler, because the thing worth
// pinning is what the loop reports back — and "the connection was held for a while" versus "the
// handshake succeeded and it dropped" is a distinction only a real connection makes.

package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/micro-teams/micro-connector/cli/protocol"
)

// server accepts WebSocket connections and behaves as told.
func server(t *testing.T, hold time.Duration) (url string, dialled func() int) {
	t.Helper()
	var mu sync.Mutex
	count := 0
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count++
		mu.Unlock()
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if hold > 0 {
			time.Sleep(hold)
		}
	}))
	t.Cleanup(s.Close)

	return "ws" + strings.TrimPrefix(s.URL, "http"), func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}
}

func TestChooseURLDecidesWhereEachAttemptGoes(t *testing.T) {
	first, firstDialled := server(t, 0)
	second, secondDialled := server(t, 0)

	attempt := 0
	conn := NewWithOptions("ws://unused.invalid/ignored", "token", "https://api.example", Options{
		ChooseURL: func() string {
			attempt++
			if attempt == 1 {
				return first
			}
			return second
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = conn.Run(ctx, func(protocol.Msg) {})

	if firstDialled() == 0 || secondDialled() == 0 {
		t.Errorf("each attempt should have gone where it was told: %d then %d",
			firstDialled(), secondDialled())
	}
}

// Without ChooseURL nothing changes: the URL passed to New is used for every attempt, which is what
// every existing consumer of this package relies on.
func TestWithoutAChooserTheConfiguredURLIsUsed(t *testing.T) {
	only, dialled := server(t, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = New(only, "token", "https://api.example").Run(ctx, func(protocol.Msg) {})

	if dialled() == 0 {
		t.Error("the configured URL was never dialled")
	}
}

// The distinction the report exists for. A route that fails to dial and a route that accepts the
// handshake then drops it are the same "not connected" from a distance, and telling them apart is
// how a caller stops choosing a proxy that cannot carry a stream.
func TestTheReportDistinguishesAFailedDialFromADroppedConnection(t *testing.T) {
	held, _ := server(t, 300*time.Millisecond)

	var mu sync.Mutex
	reports := []struct {
		url  string
		held time.Duration
		err  error
	}{}

	attempt := 0
	conn := NewWithOptions("", "token", "", Options{
		ChooseURL: func() string {
			attempt++
			if attempt == 1 {
				return "ws://127.0.0.1:1/nothing-here"
			}
			return held
		},
		Report: func(url string, held time.Duration, err error) {
			mu.Lock()
			defer mu.Unlock()
			reports = append(reports, struct {
				url  string
				held time.Duration
				err  error
			}{url, held, err})
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_ = conn.Run(ctx, func(protocol.Msg) {})

	mu.Lock()
	defer mu.Unlock()
	if len(reports) < 2 {
		t.Fatalf("expected a report per attempt, got %d", len(reports))
	}
	if reports[0].held != 0 || reports[0].err == nil {
		t.Errorf("a refused dial should report no time held and an error: %+v", reports[0])
	}
	if reports[1].held <= 0 {
		t.Errorf("a connection that was held should report how long: %+v", reports[1])
	}
	if reports[1].url != held {
		t.Errorf("the report names the wrong url: %q", reports[1].url)
	}
}

// Reconnect is the difference between "choose again" and "stop", and the difference matters more
// here than the code suggests: stopping the process is what kills the screens a machine hosts, so
// forcing a new attempt must never mean restarting anything.
func TestReconnectDialsAgainWithoutStopping(t *testing.T) {
	only, dialled := server(t, time.Hour) // holds the connection open until told otherwise

	conn := New(only, "token", "https://api.example")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() { _ = conn.Run(ctx, func(protocol.Msg) {}); close(done) }()

	// Wait for the first connection, then ask for another.
	for i := 0; i < 50 && dialled() == 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if dialled() == 0 {
		t.Fatal("never connected in the first place")
	}
	conn.Reconnect()

	for i := 0; i < 100 && dialled() < 2; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if dialled() < 2 {
		t.Errorf("Reconnect did not produce a second attempt: %d", dialled())
	}

	// And the loop is still the loop: it ends when its context does, not when Reconnect is called.
	select {
	case <-done:
		t.Error("Reconnect stopped the loop")
	default:
	}
}

// Calling it with nothing connected must be harmless — the loop is already dialling.
func TestReconnectWhileDisconnectedIsSafe(t *testing.T) {
	New("ws://127.0.0.1:1/nothing", "token", "").Reconnect()
}
