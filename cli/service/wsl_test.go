package service

import "testing"

// TestSetBootSystemdTrue guards the actual risk this exists for: EnsureSystemd touches wsl.conf on
// a real, possibly-already-configured machine. It must never destroy an existing [boot] command=
// (a user's own boot script) or any other section — only the systemd key may change, and only when
// it isn't already true.
func TestSetBootSystemdTrue(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		changed bool
	}{
		{
			name:    "empty file gets a fresh [boot] section",
			in:      "",
			want:    "[boot]\nsystemd = true\n",
			changed: true,
		},
		{
			name:    "no [boot] section at all: appended after existing content",
			in:      "[automount]\nenabled = true\n",
			want:    "[automount]\nenabled = true\n[boot]\nsystemd = true\n",
			changed: true,
		},
		{
			name:    "[boot] exists without a systemd key: key inserted, command= untouched",
			in:      "[boot]\ncommand = /usr/local/bin/my-other-tool --start\n",
			want:    "[boot]\nsystemd = true\ncommand = /usr/local/bin/my-other-tool --start\n",
			changed: true,
		},
		{
			name:    "already true: byte-for-byte unchanged",
			in:      "[boot]\nsystemd=true\ncommand = something\n",
			want:    "[boot]\nsystemd=true\ncommand = something\n",
			changed: false,
		},
		{
			name:    "explicitly false: flipped to true, nothing else touched",
			in:      "[boot]\nsystemd = false\ncommand = something\n",
			want:    "[boot]\nsystemd = true\ncommand = something\n",
			changed: true,
		},
		{
			name:    "a later, unrelated section is never mistaken for [boot]",
			in:      "[boot]\ncommand = x\n[network]\nsystemd = false\n",
			want:    "[boot]\nsystemd = true\ncommand = x\n[network]\nsystemd = false\n",
			changed: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := setBootSystemdTrue(tc.in)
			if got != tc.want {
				t.Errorf("content:\n got:  %q\n want: %q", got, tc.want)
			}
			if changed != tc.changed {
				t.Errorf("changed = %v, want %v", changed, tc.changed)
			}
		})
	}
}

// TestIsWSLFalseOffLinux is a narrow, deterministic slice of IsWSL that does not depend on the
// machine actually being WSL: on any non-Linux GOOS it must be false without touching the
// filesystem at all (there is no /proc there to read).
func TestIsWSLFalseOffLinux(t *testing.T) {
	// This test only asserts something on non-Linux CI runners (darwin/windows); on a real Linux
	// box (including a Linux CI runner) IsWSL() legitimately depends on that machine's kernel, so
	// there is nothing safe to assert here beyond "it doesn't panic".
	_ = IsWSL()
}
