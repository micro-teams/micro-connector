// Command testconn is a connector built from the library and nothing else: brand, enrolment, a
// resident link and the screen manager. It has no screen handling of its own, because screen
// handling is the library's job, which keeps a product's own connector this thin.
//
// Flags: -base, the control plane's base URL (default http://127.0.0.1:8099). It enrols, prints
// "enrolled as <machine id>", then serves screens over ws://<base>/link until SIGINT or SIGTERM.
// Example: testconn -base http://127.0.0.1:8099
package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"os"
	"os/signal"
	"syscall"

	"github.com/micro-teams/micro-connector/cli/auth"
	"github.com/micro-teams/micro-connector/cli/brand"
	"github.com/micro-teams/micro-connector/cli/screen"
	"github.com/micro-teams/micro-connector/cli/terminal"
	"github.com/micro-teams/micro-connector/cli/transport/ws"
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

	conn := ws.New("ws"+strings.TrimPrefix(*base, "http")+"/link", token, *base)
	mgr := screen.NewManager(ctx, conn, tm)
	mgr.OnScreensChanged = func(live int) { fmt.Println("screens:", live) }

	defer mgr.CloseAll()
	defer tm.KillServer()
	fmt.Println("connected; waiting for the control plane")
	_ = conn.Run(ctx, mgr.Dispatch)
}
