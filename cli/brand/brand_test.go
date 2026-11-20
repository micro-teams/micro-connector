// Tests for the brand's environment names and runtime paths.
package brand

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setHome sets the home directory os.UserHomeDir reads: $HOME, and %USERPROFILE% on Windows.
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
	}
}

// /tmp is world-writable and emptied at boot, so the runtime path is under the home directory.
func TestRuntimePathIsUnderHomeNotTmp(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Setenv("XDG_RUNTIME_DIR", "")

	got := Current.RuntimePath()
	if want := filepath.Join(home, ".local", "state", Current.RuntimeDir); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A system service has no $XDG_RUNTIME_DIR and a login shell has one; both must meet at one socket.
func TestRuntimePathIsTheSameWithOrWithoutXDGRuntimeDir(t *testing.T) {
	setHome(t, t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", "")
	service := Current.RuntimePath()

	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if shell := Current.RuntimePath(); shell != service {
		t.Fatalf("a login shell resolves %q, the service %q", shell, service)
	}
}

// A runtime dir that does not exist yet (nobody has logged in) does not matter either.
func TestRuntimePathFallsBackToHomeNotTmp(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "not-there"))
	home := t.TempDir()
	setHome(t, home)

	got := Current.RuntimePath()
	if want := filepath.Join(home, ".local", "state", Current.RuntimeDir); got != want {
		t.Fatalf("with no runtime dir: got %q, want %q", got, want)
	}
}

// Without a home directory the path is under the temp dir.
func TestRuntimePathStillAnswersWithNothingSet(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	setHome(t, "")
	if got := Current.RuntimePath(); got == "" {
		t.Fatal("returned no path at all")
	}
}

// XDGRuntimePath is under $XDG_RUNTIME_DIR, and empty without it.
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

// LegacyRuntimePath is under the temp dir.
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
