// Command testconn is a connector built from the library and nothing else.
//
// It exists to keep two promises honest. The first is that a product's own code is thin: everything
// below is brand, plumbing and one command — there is no screen handling here, because screen
// handling is not a product's job. The second is that the one-shot shape works: this drives a
// screen over HTTP polling, with no daemon, no WebSocket and no service to install, which is what a
// provisioning tool needs and what MicroTeams' resident connector would otherwise have hidden.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/micro-teams/micro-connector/cli/brand"
	"github.com/micro-teams/micro-connector/cli/screen"
	"github.com/micro-teams/micro-connector/cli/terminal"
	"github.com/micro-teams/micro-connector/cli/transport/httppoll"
)

func main() {
	base := flag.String("base", "http://127.0.0.1:8099", "control plane base URL")
	flag.Parse()

	// Who this connector is. Every path and environment variable follows from this one value, and a
	// product that skips it does not fail loudly — it quietly uses someone else's.
	brand.Current = brand.Brand{
		Name:        "testconn",
		EnvPrefix:   "TESTCONN",
		ConfigDir:   "testconn",
		RuntimeDir:  "testconn",
		ServiceName: "testconn",
		EnrollBase:  "/machine/enroll",
		BinaryBase:  "/connector/latest",
	}

	machineID, token, err := enroll(*base)
	if err != nil {
		fmt.Fprintln(os.Stderr, "enroll:", err)
		os.Exit(1)
	}
	fmt.Println("enrolled as", machineID)

	tm, err := terminal.NewManager()
	if err != nil {
		fmt.Fprintln(os.Stderr, "terminal:", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	conn := httppoll.New(&http.Client{Timeout: 30 * time.Second}, *base, "/bus/inbox", "/bus/outbox", machineID, token)
	mgr := screen.NewManager(ctx, conn, tm)
	mgr.OnScreensChanged = func(live int) { fmt.Println("screens:", live) }

	defer mgr.CloseAll()
	defer tm.KillServer()
	fmt.Println("connected; waiting for the control plane")
	_ = conn.Run(ctx, mgr.Dispatch)
}

// enroll walks the two-step exchange: ask, then wait to be approved. A real product would show the
// code to a human here; this one just waits.
func enroll(base string) (machineID, token string, err error) {
	var start struct {
		PollToken string `json:"pollToken"`
	}
	if err = postJSON(base+brand.Current.EnrollBase+"/start", map[string]any{}, &start); err != nil {
		return "", "", err
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var poll struct {
			Status    string `json:"status"`
			MachineID string `json:"machineId"`
			Token     string `json:"token"`
		}
		if err = postJSON(base+brand.Current.EnrollBase+"/poll",
			map[string]any{"pollToken": start.PollToken}, &poll); err != nil {
			return "", "", err
		}
		if poll.Status == "approved" {
			return poll.MachineID, poll.Token, nil
		}
		time.Sleep(time.Second)
	}
	return "", "", fmt.Errorf("enrolment was never approved")
}

func postJSON(url string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := http.Post(url, "application/json", strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
