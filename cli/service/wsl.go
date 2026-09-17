package service

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// IsWSL reports whether this process is running inside Windows Subsystem for Linux. WSL2's kernel
// identifies itself in /proc/sys/kernel/osrelease (e.g. "...-microsoft-standard-WSL2"), which is the
// standard, documented way to detect it — there is no syscall or env var for this.
func IsWSL() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return false
	}
	s := strings.ToLower(string(b))
	return strings.Contains(s, "microsoft") || strings.Contains(s, "wsl")
}

// HasSystemd reports whether systemd is actually running as this machine's init (PID 1), the
// standard check freedesktop.org documents: systemd bind-mounts /run/systemd/system into every
// mount namespace it manages, so its presence means systemd — not just "installed", but "is init
// right now". WSL2 does not have this by default; it needs an explicit opt-in (see EnsureSystemd).
func HasSystemd() bool {
	_, err := os.Stat("/run/systemd/system")
	return err == nil
}

// SystemdStatus is what EnsureSystemd found and did.
type SystemdStatus int

const (
	// SystemdActive means systemd is already PID 1 — nothing to do, proceed with the normal
	// systemd --user install path.
	SystemdActive SystemdStatus = iota
	// SystemdNotApplicable means this isn't WSL (or isn't Linux at all) and HasSystemd() is false:
	// EnsureSystemd only knows how to fix the WSL case, so it made no change. A caller on real Linux
	// without systemd needs a different init-system backend entirely, which is outside this helper.
	SystemdNotApplicable
	// SystemdNeedsRestart means wsl.conf was just changed to enable systemd, but that only takes
	// effect on the NEXT time this WSL distro boots. Nothing inside the current WSL session can
	// trigger that restart (`wsl --shutdown` has to run on the Windows side); the caller must tell
	// the operator to run it and re-invoke afterward.
	SystemdNeedsRestart
)

// bootSystemdRe matches an existing "systemd = <value>" line inside wsl.conf's [boot] section —
// case-insensitive key per WSL's own ini parser, optional spaces around "=", trailing comment
// allowed. Capturing the whole line lets EnsureSystemd replace only this one key and leave every
// other line (including unrelated tools' own boot config) byte-for-byte untouched.
var bootSystemdRe = regexp.MustCompile(`(?i)^\s*systemd\s*=\s*(\S+)`)

// EnsureSystemd makes systemd available as WSL's PID 1 if this is WSL and it is not already —
// the prerequisite for reusing the exact same systemd --user install path this package already
// uses on real Linux. It edits only the "systemd" key inside wsl.conf's [boot] section (a boolean,
// see the doc comment on why that is safe to write unconditionally, unlike [boot]'s free-text
// "command" key which may already hold a user's own script and would be destroyed by a naive
// overwrite). Every other line of wsl.conf — including a pre-existing "command=" — is preserved.
//
// Does not, and cannot, restart WSL itself: a change here only takes effect on this distro's next
// boot (`wsl --shutdown` from Windows, then reopening it). Call this, check the returned status,
// and if it is SystemdNeedsRestart, stop and tell the operator to restart WSL and re-run — do not
// proceed to install a systemd --user unit in the same process, since systemd is not actually
// running yet and the install would fail confusingly.
func EnsureSystemd() (SystemdStatus, error) {
	if !IsWSL() {
		return SystemdNotApplicable, nil
	}
	if HasSystemd() {
		return SystemdActive, nil
	}

	const path = "/etc/wsl.conf"
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return SystemdNotApplicable, fmt.Errorf("service: read %s: %w", path, err)
	}

	updated, changed := setBootSystemdTrue(string(existing))
	if !changed {
		// Already says true (but HasSystemd() is false — e.g. mid-boot or a stale check); nothing
		// for us to change, but it is not active yet either.
		return SystemdActive, nil
	}
	// 0644: wsl.conf is world-readable system config, same as it would be if a person edited it.
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return SystemdNotApplicable, fmt.Errorf("service: write %s: %w", path, err)
	}
	return SystemdNeedsRestart, nil
}

// setBootSystemdTrue returns wsl.conf's content with [boot]'s systemd key forced to true, and
// whether it actually changed anything. It never touches any other key (including [boot]'s own
// "command="), and never touches any other section.
func setBootSystemdTrue(content string) (result string, changed bool) {
	lines := strings.Split(content, "\n")
	inBoot := false
	haveBootSection := false
	sectionRe := regexp.MustCompile(`^\s*\[([^\]]+)\]\s*$`)

	for i, line := range lines {
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			inBoot = strings.EqualFold(strings.TrimSpace(m[1]), "boot")
			if inBoot {
				haveBootSection = true
			}
			continue
		}
		if !inBoot {
			continue
		}
		if m := bootSystemdRe.FindStringSubmatch(line); m != nil {
			if strings.EqualFold(m[1], "true") {
				return content, false // already set — leave the file untouched
			}
			lines[i] = "systemd = true"
			return strings.Join(lines, "\n"), true
		}
	}

	// No existing "systemd=" key under [boot]. Append one — a new [boot] section at the end if
	// there wasn't one at all, or one more line under the existing (now-known-empty-of-this-key)
	// [boot] section.
	if !haveBootSection {
		if len(content) > 0 && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		return content + "[boot]\nsystemd = true\n", true
	}
	// There was a [boot] section but no systemd= line in it: insert right after the header, so it
	// reads naturally rather than trailing after whatever the section's last key happens to be.
	out := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		out = append(out, line)
		if m := sectionRe.FindStringSubmatch(line); m != nil && strings.EqualFold(strings.TrimSpace(m[1]), "boot") {
			out = append(out, "systemd = true")
		}
	}
	return strings.Join(out, "\n"), true
}

// EnableLinger makes the current user's systemd --user instance start at boot and outlive their
// login sessions — required for a systemd --user unit to run unattended, on WSL exactly as on real
// Linux. Idempotent: `loginctl enable-linger` on an already-lingering user is a no-op success.
func EnableLinger() error {
	u := os.Getenv("USER")
	if u == "" {
		return fmt.Errorf("service: enable linger: $USER is unset")
	}
	out, err := exec.Command("loginctl", "enable-linger", u).CombinedOutput()
	if err != nil {
		return fmt.Errorf("service: enable linger for %s: %w: %s", u, err, strings.TrimSpace(string(out)))
	}
	return nil
}
