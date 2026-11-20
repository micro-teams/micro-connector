// WSL support. A per-user service needs systemd, which WSL2 runs only when /etc/wsl.conf asks for it
// in its [boot] section, and which runs a user's units unattended only with lingering on.
//
//	IsWSL() bool          the kernel says Microsoft/WSL (/proc/sys/kernel/osrelease).
//	HasSystemd() bool     systemd is PID 1 (/run/systemd/system exists).
//	EnsureSystemd() (SystemdStatus, error)
//	                      on WSL without systemd, sets [boot] systemd = true in /etc/wsl.conf (root),
//	                      changing no other line: SystemdNeedsRestart, since only the distro's next
//	                      start (`wsl --shutdown` on Windows) applies it, and installing a unit before
//	                      then cannot work. SystemdActive when systemd already runs (or wsl.conf
//	                      already asks for it); SystemdNotApplicable off WSL.
//	EnableLinger() error  `loginctl enable-linger $USER`, so the user's units run without a login.
//
// Example:
//
//	switch st, err := service.EnsureSystemd(); {
//	case err != nil: return err
//	case st == service.SystemdNeedsRestart: fmt.Println("run `wsl --shutdown` in Windows, then again")
//	}
package service

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

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

func HasSystemd() bool {
	_, err := os.Stat("/run/systemd/system")
	return err == nil
}

type SystemdStatus int

const (
	SystemdActive SystemdStatus = iota
	SystemdNotApplicable
	SystemdNeedsRestart
)

var bootSystemdRe = regexp.MustCompile(`(?i)^\s*systemd\s*=\s*(\S+)`)

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
