package brand

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The socket lived in /tmp until a reboot proved twice why it must not.
//
// /tmp is world-writable, so "<brand>-<uid>" is a name anything can take first, and it is a tmpfs,
// so it is empty at boot and the race is run again every time the machine starts. The loser of that
// race cannot open its own socket, and what a person sees is an agent that will not open.
func TestRuntimePathPrefersTheRuntimeDir(t *testing.T) {
	run := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", run)

	got := Current.RuntimePath()
	if want := filepath.Join(run, Current.RuntimeDir); got != want {
		t.Fatalf("with XDG_RUNTIME_DIR set: got %q, want %q", got, want)
	}
}

// Set but pointing nowhere is the case a system service hits: systemd exports nothing, or logind
// has not made /run/user/<uid> yet because nobody has logged in. Trusting the variable blindly
// would put the socket in a directory that does not exist.
func TestRuntimePathFallsBackToHomeNotTmp(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "not-there"))
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := Current.RuntimePath()
	if want := filepath.Join(home, ".local", "state", Current.RuntimeDir); got != want {
		t.Fatalf("with no runtime dir: got %q, want %q", got, want)
	}
}

// And with neither, it still returns something rather than failing to start.
func TestRuntimePathStillAnswersWithNothingSet(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", "")
	if got := Current.RuntimePath(); got == "" {
		t.Fatal("returned no path at all")
	}
}

// An in-place update must not strand the sessions the previous build is hosting: the socket it
// started lives under the temp dir, and this is the path that still points at it.
func TestLegacyRuntimePathIsTheOldTmpOne(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	legacy := Current.LegacyRuntimePath()
	if legacy == Current.RuntimePath() {
		t.Fatal("the legacy path is the current one — an update would find no old socket to keep")
	}
	if !strings.HasPrefix(legacy, os.TempDir()) {
		t.Fatalf("the old socket was under the temp dir; this points at %q", legacy)
	}
}
