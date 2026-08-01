package update

import (
	"testing"

	"github.com/micro-teams/micro-connector/cli/brand"
)

func TestPlatformDir(t *testing.T) {
	cases := []struct {
		goos, goarch, want string
	}{
		{"linux", "amd64", "linux-amd64"},
		{"linux", "arm64", "linux-arm64"},
		{"darwin", "amd64", "darwin-amd64"},
		{"darwin", "arm64", "darwin-arm64"},
	}
	for _, c := range cases {
		got, err := platformDir(c.goos, c.goarch)
		if err != nil {
			t.Fatalf("platformDir(%q,%q) error: %v", c.goos, c.goarch, err)
		}
		if got != c.want {
			t.Errorf("platformDir(%q,%q) = %q, want %q", c.goos, c.goarch, got, c.want)
		}
	}
}

func TestPlatformDirUnsupported(t *testing.T) {
	if _, err := platformDir("windows", "amd64"); err == nil {
		t.Error("expected error for windows")
	}
	if _, err := platformDir("linux", "riscv64"); err == nil {
		t.Error("expected error for riscv64")
	}
}

// The published path is part of a product's identity, so the test says which product it is —
// exactly as a binary does at startup. A test that forgot would be asserting against the library's
// deliberately generic default, which is the mistake this arrangement exists to make visible.
func init() {
	brand.Current = brand.Brand{Name: "microteams", BinaryBase: "/connector/latest"}
}

func TestBinaryURL(t *testing.T) {
	cases := []struct {
		base, dir, want string
	}{
		{
			"https://microteams.ruc.edu.cn/api", "linux-x86_64",
			"https://microteams.ruc.edu.cn/connector/latest/linux-x86_64/microteams",
		},
		{
			"http://127.0.0.1:8080", "darwin-arm64",
			"http://127.0.0.1:8080/connector/latest/darwin-arm64/microteams",
		},
		{
			"https://example.com/", "linux-aarch64",
			"https://example.com/connector/latest/linux-aarch64/microteams",
		},
	}
	for _, c := range cases {
		got, err := binaryURL(c.base, c.dir)
		if err != nil {
			t.Fatalf("binaryURL(%q,%q) error: %v", c.base, c.dir, err)
		}
		if got != c.want {
			t.Errorf("binaryURL(%q,%q) = %q, want %q", c.base, c.dir, got, c.want)
		}
	}
}

// The published path is part of a product's identity, so the test says which product it is —
// exactly as a binary does at startup. A test that forgot would be asserting against the library's
// deliberately generic default, which is the mistake this arrangement exists to make visible.
func init() {
	brand.Current = brand.Brand{Name: "microteams", BinaryBase: "/connector/latest"}
}

func TestBinaryURLBadBase(t *testing.T) {
	if _, err := binaryURL("not-a-url", "linux-x86_64"); err == nil {
		t.Error("expected error for base without host")
	}
}
