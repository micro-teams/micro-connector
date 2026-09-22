package screen

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/micro-teams/micro-connector/cli/protocol"
)

// last returns the single message conn.all() holds, failing loudly if that's not exactly one —
// each handler here is called directly (not through Dispatch's "go m.fileWrite(msg)"), so its
// Send happens synchronously before the call returns.
func last(t *testing.T, conn *recorder) protocol.Msg {
	t.Helper()
	sent := conn.all()
	if len(sent) != 1 {
		t.Fatalf("got %d messages, want exactly 1: %v", len(sent), sent)
	}
	return sent[0]
}

func TestFileWriteCreatesParentsAndIsReadableBack(t *testing.T) {
	conn := &recorder{}
	m := &Manager{conn: conn}
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "settings.json")

	content := []byte(`{"env":{}}`)
	m.fileWrite(protocol.Msg{ID: "1", Path: path, Data: base64.StdEncoding.EncodeToString(content)})

	res := last(t, conn)
	if res.T != "file.write.result" || res.Error != "" {
		t.Fatalf("got %+v", res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("not written: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("content = %q, want %q", got, content)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeSupportsPosixPerm() && fi.Mode().Perm() != fileWriteMode {
		t.Fatalf("mode = %o, want %o — a credential file readable by others", fi.Mode().Perm(), fileWriteMode)
	}
}

func TestFileWriteReplacesExistingContentAtomically(t *testing.T) {
	conn := &recorder{}
	m := &Manager{conn: conn}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	m.fileWrite(protocol.Msg{ID: "1", Path: path, Data: base64.StdEncoding.EncodeToString([]byte("new"))})
	last(t, conn)

	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Fatalf("content = %q, want %q", got, "new")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf(".tmp sibling left behind: %v", err)
	}
}

func TestFileReadOfMissingFileIsEmptyNotAnError(t *testing.T) {
	conn := &recorder{}
	m := &Manager{conn: conn}
	m.fileRead(protocol.Msg{ID: "1", Path: filepath.Join(t.TempDir(), "does-not-exist")})

	res := last(t, conn)
	if res.Error != "" {
		t.Fatalf("a missing file should not be an error (matches `cat 2>/dev/null || true`): %s", res.Error)
	}
	if res.Data != "" {
		t.Fatalf("Data = %q, want empty", res.Data)
	}
}

func TestFileReadRoundTrips(t *testing.T) {
	conn := &recorder{}
	m := &Manager{conn: conn}
	path := filepath.Join(t.TempDir(), "existing.json")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.fileRead(protocol.Msg{ID: "1", Path: path})

	res := last(t, conn)
	got, err := base64.StdEncoding.DecodeString(res.Data)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q, want %q", got, "hello")
	}
}

func TestFileRemoveOfMissingFileIsSuccessNotAnError(t *testing.T) {
	conn := &recorder{}
	m := &Manager{conn: conn}
	m.fileRemove(protocol.Msg{ID: "1", Path: filepath.Join(t.TempDir(), "does-not-exist")})

	res := last(t, conn)
	if res.Error != "" {
		t.Fatalf("removing an already-gone file should succeed (matches `rm -f`): %s", res.Error)
	}
}

func TestFileRemoveDeletesAnExistingFile(t *testing.T) {
	conn := &recorder{}
	m := &Manager{conn: conn}
	path := filepath.Join(t.TempDir(), "gone-soon.json")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.fileRemove(protocol.Msg{ID: "1", Path: path})

	last(t, conn)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("still there: %v", err)
	}
}

func TestHomeDirAnswersWithARealDirectory(t *testing.T) {
	conn := &recorder{}
	m := &Manager{conn: conn}
	m.homeDir(protocol.Msg{ID: "1"})

	res := last(t, conn)
	if res.Error != "" {
		t.Fatalf("homedir.result error: %s", res.Error)
	}
	if res.Path == "" {
		t.Fatal("Path is empty")
	}
	if fi, err := os.Stat(res.Path); err != nil || !fi.IsDir() {
		t.Fatalf("%q is not a real, existing directory: %v", res.Path, err)
	}
}

// runtimeSupportsPosixPerm reports whether this OS's os.FileMode.Perm() is a real POSIX permission
// bit set. Windows' is not (it reflects the read-only attribute instead), so asserting an exact
// 0600 there would fail for a reason that has nothing to do with this code's correctness.
func runtimeSupportsPosixPerm() bool { return os.PathSeparator == '/' }
