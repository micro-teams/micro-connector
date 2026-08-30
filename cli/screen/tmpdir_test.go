package screen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/micro-teams/micro-connector/cli/brand"
)

// A hosted program must scratch in a directory this user owns.
//
// The bug this pins: programs name their scratch space after the uid — Claude Code uses
// /tmp/claude-$UID — and /tmp is world-writable, so that name is whoever gets there first. On a
// machine where /tmp is a tmpfs the race is re-run at every boot, and the morning this was written
// root had won it: the agent started, could not write, and was dead about a minute later. Every
// time, and looking from the outside like "the agent will not open".
func TestScreenTmpDirIsOursAndNotUnderTmp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", home)

	dir, err := screenTmpDir("s1234")
	if err != nil {
		t.Fatalf("no scratch directory: %v", err)
	}
	if want := filepath.Join(brand.Current.RuntimePath(), "tmp", "s1234"); dir != want {
		t.Fatalf("got %q, want %q", dir, want)
	}
	if !strings.HasPrefix(dir, home) {
		t.Fatalf("scratch is outside this user's own space: %q", dir)
	}

	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("not created: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Fatalf("mode is %o — another user can read what an agent scratched down", perm)
	}
}

// Two screens do not share one scratch directory: one agent's leftovers are not another's to read.
func TestEachScreenGetsItsOwn(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", t.TempDir())

	a, err := screenTmpDir("sAAA")
	if err != nil {
		t.Fatal(err)
	}
	b, err := screenTmpDir("sBBB")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("both screens got %q", a)
	}
}
