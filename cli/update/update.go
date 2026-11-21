// Package update replaces the running binary with the one the control plane publishes. It
// downloads the binary for this platform next to the running executable, proves it runs, and swaps
// it in atomically. Handing the process over to the new binary (re-exec, so the screens' tmux
// keeps running) is the caller's part.
//
// Binaries are published at <origin of base><BinaryBase>/<os>-<arch>/<name>, with <os>-<arch> one
// of linux-amd64, linux-arm64, darwin-amd64, darwin-arm64.
//
//	PlatformDir() (string, error)          <os>-<arch> of this process.
//	Fetch(ctx, base, name) (string, error) downloads binary `name` (e.g. brand.Current.Name, or a
//	                                       helper binary published beside it) into a temp file in
//	                                       the executable's directory, makes it executable, and
//	                                       checks `<file> --version` succeeds; returns the file.
//	Replace(tmpPath, selfPath) error       renames tmpPath onto selfPath (atomic: same directory).
//	SelfPath() (string, error)             the running executable, symlinks resolved.
//
// Example:
//
//	tmp, err := update.Fetch(ctx, cfg.Base, brand.Current.Name)
//	self, err := update.SelfPath()
//	err = update.Replace(tmp, self)
//	syscall.Exec(self, os.Args, os.Environ())
package update

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/micro-teams/micro-connector/cli/brand"
)

func platformDir(goos, goarch string) (string, error) {
	if goos != "linux" && goos != "darwin" {
		return "", fmt.Errorf("update: no published binary for OS %q", goos)
	}
	if goarch != "amd64" && goarch != "arm64" {
		return "", fmt.Errorf("update: no published binary for architecture %q", goarch)
	}
	return goos + "-" + goarch, nil
}

func PlatformDir() (string, error) { return platformDir(runtime.GOOS, runtime.GOARCH) }

func binaryURL(base, dir, name string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("update: bad base %q: %v", base, err)
	}
	return u.Scheme + "://" + u.Host + brand.Current.BinaryBase + "/" + dir + "/" + name, nil
}

func Fetch(ctx context.Context, base, name string) (string, error) {
	dir, err := PlatformDir()
	if err != nil {
		return "", err
	}
	rawURL, err := binaryURL(base, dir, name)
	if err != nil {
		return "", err
	}
	self, err := SelfPath()
	if err != nil {
		return "", fmt.Errorf("update: locate self: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(self), "."+name+"-update-*")
	if err != nil {
		return "", fmt.Errorf("update: temp file: %w", err)
	}
	if err := download(ctx, rawURL, tmp); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("update: close: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("update: chmod: %w", err)
	}
	// A truncated download, an error page or a binary for another platform fails here, before it
	// can replace anything.
	vctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := exec.CommandContext(vctx, tmp.Name(), "--version").Run(); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("update: the downloaded %s does not run: %w", name, err)
	}
	return tmp.Name(), nil
}

func download(ctx context.Context, rawURL string, w io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("update: request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("update: download %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("update: download %s: %s", rawURL, resp.Status)
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("update: download %s: %w", rawURL, err)
	}
	return nil
}

func Replace(tmpPath, selfPath string) error {
	if err := os.Rename(tmpPath, selfPath); err != nil {
		return fmt.Errorf("update: replace %s: %w", selfPath, err)
	}
	return nil
}

func SelfPath() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(self)
}
