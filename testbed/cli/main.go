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
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/micro-teams/micro-connector/cli/auth"
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

	// Enrolment comes from the library: a product should not be writing its own two-step handshake,
	// and this test connector deliberately uses the same one MicroTeams does.
	res, err := auth.Login(context.Background(), *base, func(url string) {
		fmt.Println("approve at:", url)
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "enroll:", err)
		os.Exit(1)
	}
	machineID, token := res.MachineID, res.Token
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
