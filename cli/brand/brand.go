// Package brand holds the handful of names that make this connector one product rather than
// another: what the binary is called, what its environment variables are prefixed with, where its
// config and runtime files live, and which endpoints it enrolls against.
//
// They are collected here because they are the entire difference. Everything else in this connector
// — driving a terminal, hosting an applet, keeping a screen alive, updating itself — is the same
// work regardless of whose product it is, and a literal "microteams" buried in a path is what turns
// shared code into a fork.
//
// One value per process. A connector is one product for its whole life, so this is a constant that
// happens to be set at startup rather than a parameter threaded through every call. Set it once,
// before anything else runs; changing it later is a programming error, not a feature.
package brand

import (
	"os"
	"path/filepath"
	"strconv"
)

type Brand struct {
	// Name is the binary's name, and the prefix on everything it says to a terminal.
	Name string
	// EnvPrefix prefixes the environment variables this connector reads and injects into screens
	// (e.g. "MICROTEAMS" -> MICROTEAMS_API, MICROTEAMS_TOKEN, MICROTEAMS_SCREEN).
	EnvPrefix string
	// ConfigDir is the directory under the user's config home holding config.json.
	ConfigDir string
	// RuntimeDir names the per-user runtime directory that holds the private tmux socket. It must
	// differ between products: two connectors sharing it would each see the other's sessions, and
	// tearing one down would take the other's screens with it.
	RuntimeDir string
	// ServiceName is the system service's name. It must not drift: uninstalling by the wrong name
	// stops nothing and reports success.
	ServiceName string
	// ServiceDisplayName and ServiceDescription are what a person reads in their init system.
	ServiceDisplayName string
	ServiceDescription string
	// EnrollBase is the control plane's enrollment path, e.g. "/machine/enroll", under which
	// "/start" and "/poll" live.
	EnrollBase string
	// BinaryBase is where a published binary lives on the control plane's origin, e.g.
	// "/connector/latest", followed by "/<os>-<arch>/<Name>".
	BinaryBase string
}

// Current is the brand this process is. A product sets it at startup, before anything reads a path
// or an environment variable. The default is deliberately generic and deliberately not anyone's
// product: a connector that forgot to say who it is should look obviously unconfigured rather than
// quietly borrow another product's socket and config.
var Current = Brand{
	Name:               "connector",
	EnvPrefix:          "CONNECTOR",
	ConfigDir:          "connector",
	RuntimeDir:         "connector",
	ServiceName:        "connector",
	ServiceDisplayName: "Connector",
	ServiceDescription: "Connects this machine to its control plane.",
	EnrollBase:         "/machine/enroll",
	BinaryBase:         "/connector/latest",
}

// Env returns a full environment variable name, e.g. Env("TOKEN") -> "MICROTEAMS_TOKEN".
func (b Brand) Env(name string) string { return b.EnvPrefix + "_" + name }

// Getenv reads one of this brand's environment variables.
func (b Brand) Getenv(name string) string { return os.Getenv(b.Env(name)) }

// RuntimePath is the per-user runtime directory: a STABLE path, not a fresh temp dir. The tmux
// server outlives the connector process, so a restarted or self-updated connector must arrive back
// at the same socket to find the sessions it left behind.
//
// It is deliberately NOT under /tmp any more, and both reasons were paid for on a live machine:
//
//   - /tmp is world-writable, so "<brand>-<uid>" is a name anything can take first. Whoever wins
//     the race owns the directory, and the loser cannot open its own socket.
//   - /tmp is a tmpfs on a normal systemd box, so it is empty after a reboot. That is survivable
//     for the socket (the sessions are gone too) but it means the connector comes back to a
//     directory it does not own if something else got there first during boot.
//
// It must also be the SAME path for every process of the user, whatever environment that process
// was started with, because the service and the commands a person runs in a shell meet at this
// socket. It used to prefer $XDG_RUNTIME_DIR, and that split them on a live machine: the service, a
// systemd system unit, has no $XDG_RUNTIME_DIR and hosted its screens under $HOME, while a login
// shell has /run/user/<uid>, so `status` looked there, always reported no screens, and left a stray
// directory behind. So the path is under $HOME alone, which every process of the user agrees on.
// Only without a home does it use the temp dir, so a process with no home still runs.
func (b Brand) RuntimePath() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "state", b.RuntimeDir)
	}
	return filepath.Join(os.TempDir(), b.RuntimeDir+"-"+strconv.Itoa(os.Getuid()))
}

// XDGRuntimePath is where RuntimePath pointed for a process that had $XDG_RUNTIME_DIR (a user
// service, a connector run from a shell), or "" for one that has none. Kept for the same reason as
// LegacyRuntimePath: an in-place update must find the tmux server such a process left running.
func (b Brand) XDGRuntimePath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, b.RuntimeDir)
	}
	return ""
}

// LegacyRuntimePath is where RuntimePath used to be: under the temp dir. Kept so an in-place update
// can find a tmux server started by the previous build instead of stranding its sessions on a
// socket nobody looks at any more — which is the failure the stable-path comment above is about.
func (b Brand) LegacyRuntimePath() string {
	return filepath.Join(os.TempDir(), b.RuntimeDir+"-"+strconv.Itoa(os.Getuid()))
}
