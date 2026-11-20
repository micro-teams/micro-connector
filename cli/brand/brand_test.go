package brand

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setHome overrides the current user's home directory for the duration of the test, on whichever
// platform's own convention os.UserHomeDir actually reads: $HOME everywhere except Windows, where
// it is %USERPROFILE% instead. Setting only HOME leaves a Windows test reading the real runner's
// profile dir, not the fake one — that mismatch, not a RuntimePath bug, is what a naive HOME-only
// t.Setenv would fail on there.
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
	}
}

// The socket lived in /tmp until a reboot proved twice why it must not.
//
// /tmp is world-writable, so "<brand>-<uid>" is a name anything can take first, and it is a tmpfs,
// so it is empty at boot and the race is run again every time the machine starts. The loser of that
// race cannot open its own socket, and what a person sees is an agent that will not open.
func TestRuntimePathIsUnderHomeNotTmp(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Setenv("XDG_RUNTIME_DIR", "")

	got := Current.RuntimePath()
	if want := filepath.Join(home, ".local", "state", Current.RuntimeDir); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// The service and a person's shell must meet at one socket. The service, a systemd system unit,
// has no $XDG_RUNTIME_DIR; a login shell has /run/user/<uid>. When the path followed that variable,
// `status` in a shell looked in /run/user/<uid>, reported no screens while the service hosted
// several under $HOME, and left a stray directory behind.
func TestRuntimePathIsTheSameWithOrWithoutXDGRuntimeDir(t *testing.T) {
	setHome(t, t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", "")
	service := Current.RuntimePath()

	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if shell := Current.RuntimePath(); shell != service {
		t.Fatalf("a login shell resolves %q, the service %q", shell, service)
	}
}

// A runtime dir set but pointing nowhere is the case a system service hits at boot: logind has not
// made /run/user/<uid> yet because nobody has logged in. The path does not depend on it at all.
func TestRuntimePathFallsBackToHomeNotTmp(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "not-there"))
	home := t.TempDir()
	setHome(t, home)

	got := Current.RuntimePath()
	if want := filepath.Join(home, ".local", "state", Current.RuntimeDir); got != want {
		t.Fatalf("with no runtime dir: got %q, want %q", got, want)
	}
}

// And with neither, it still returns something rather than failing to start.
func TestRuntimePathStillAnswersWithNothingSet(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	setHome(t, "")
	if got := Current.RuntimePath(); got == "" {
		t.Fatal("returned no path at all")
	}
}

// Where a build that followed $XDG_RUNTIME_DIR put the socket, so an update can find it; nothing
// for a process without one.
func TestXDGRuntimePathIsWhereTheRuntimeDirPointed(t *testing.T) {
	run := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", run)
	if got, want := Current.XDGRuntimePath(), filepath.Join(run, Current.RuntimeDir); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	if got := Current.XDGRuntimePath(); got != "" {
		t.Fatalf("without XDG_RUNTIME_DIR: got %q, want none", got)
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
