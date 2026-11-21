// Package service installs and controls a connector as a service of the machine's init system
// (systemd, OpenRC, launchd, the Windows Service Manager) through kardianos/service. What the
// service does is the product's: it supplies a Runner.
//
// Variants. An unprivileged install is a per-user service (systemd --user, a launchd LaunchAgent);
// a root install is a system service, which under sudo runs as the invoking user so the config and
// the screens stay in that user's home. Windows has only system services. Acting on an existing
// service uses the variant that is installed, whoever asks: an ordinary user stopping a service
// that was installed with sudo acts on the system one.
//
// Screens outlive the service. The systemd unit uses KillMode=process, so stopping, restarting or
// crashing the connector ends only the connector and never the tmux server it started; it restarts
// 5 s after a crash, and a user unit starts with the user's session (default.target). tmux leaves
// launchd's process group by itself.
//
//	Runner                       func(ctx) error: the work; runs until ctx is cancelled.
//	New(cfgPath, run) (ksvc.Service, error)
//	                             the service for this process's privilege; it launches
//	                             `<binary> run --config <cfgPath>`. run may be nil to only control it.
//	Control(cfgPath, action) error         install, uninstall, start, stop or restart, for this
//	                                       process's privilege (what installing wants).
//	ControlInstalled(cfgPath, action) error the same against the installed variant; ErrNotInstalled
//	                                        when there is none.
//	RunForeground(cfgPath, run) error      runs the work in this process, as the service manager does.
//	Installed(cfgPath) (userService, found bool)
//	Status(cfgPath) (string, error)        "running", "stopped", "not installed" or "unknown".
//	UnitCurrent(cfgPath) (bool, error)     whether an installed systemd unit is the one this build
//	                                       writes; false means reinstalling it is needed for screens
//	                                       to survive a stop. Always true off systemd.
//
// Example:
//
//	if err := service.Control(cfgPath, "install"); err != nil { ... }
//	if err := service.ControlInstalled(cfgPath, "restart"); err != nil { ... }
package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	ksvc "github.com/kardianos/service"

	"github.com/micro-teams/micro-connector/cli/brand"
)

type Runner func(ctx context.Context) error

var ErrNotInstalled = errors.New("no connector service is installed on this machine")

type program struct {
	run    Runner
	cancel context.CancelFunc
	done   chan struct{}
}

func (p *program) Start(_ ksvc.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	go func() {
		defer close(p.done)
		if err := p.run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("%s: service exited: %v", brand.Current.Name, err)
		}
	}()
	return nil
}

// stopWait bounds how long a stop waits for the work to wind down before the process exits anyway.
const stopWait = 6 * time.Second

func (p *program) Stop(_ ksvc.Service) error {
	if p.cancel == nil {
		return nil
	}
	p.cancel()
	select {
	case <-p.done:
		return nil
	case <-time.After(stopWait):
		return fmt.Errorf("%s: the service did not stop within %s", brand.Current.Name, stopWait)
	}
}

func New(cfgPath string, run Runner) (ksvc.Service, error) {
	return newService(cfgPath, runtime.GOOS != "windows" && os.Geteuid() != 0, run)
}

// unit is the systemd unit for a variant.
func unit(userService bool) string {
	wantedBy := "multi-user.target"
	if userService {
		wantedBy = "default.target"
	}
	return `[Unit]
Description={{Description}}
ConditionFileIsExecutable={{Path | cmdEscape}}
{{range Dependencies}}{{.}}
{{end}}
[Service]
StartLimitInterval=5
StartLimitBurst=10
ExecStart={{Path | cmdEscape}}{{range Arguments}} {{. | cmd}}{{end}}
{{if UserName}}User={{UserName}}
{{end}}{{if Restart}}Restart={{Restart}}
{{end}}RestartSec=5
KillMode=process
EnvironmentFile=-/etc/sysconfig/{{Name}}

{{range EnvVars}}{{.}}
{{end}}[Install]
WantedBy=` + wantedBy + "\n"
}

func newService(cfgPath string, userService bool, run Runner) (ksvc.Service, error) {
	cfg := &ksvc.Config{
		Name:        brand.Current.ServiceName,
		DisplayName: brand.Current.ServiceDisplayName,
		Description: brand.Current.ServiceDescription,
		Arguments:   []string{"run", "--config", cfgPath},
		Option:      ksvc.KeyValue{"SystemdScript": unit(userService)},
	}
	switch {
	case userService:
		cfg.Option["UserService"] = true
	case runtime.GOOS != "windows":
		if u := os.Getenv("SUDO_USER"); u != "" && u != "root" {
			cfg.UserName = u
		}
	}
	return ksvc.New(&program{run: run}, cfg)
}

func Control(cfgPath, action string) error {
	s, err := New(cfgPath, nil)
	if err != nil {
		return err
	}
	return ksvc.Control(s, action)
}

func ControlInstalled(cfgPath, action string) error {
	userService, found := Installed(cfgPath)
	if !found {
		return ErrNotInstalled
	}
	s, err := newService(cfgPath, userService, nil)
	if err != nil {
		return err
	}
	return ksvc.Control(s, action)
}

func RunForeground(cfgPath string, run Runner) error {
	s, err := New(cfgPath, run)
	if err != nil {
		return err
	}
	return s.Run()
}

// resolveVariant picks the installed variant, preferring preferUser when both are installed.
// probe(v) is nil when variant v is installed.
func resolveVariant(preferUser bool, probe func(userService bool) error) (userService, found bool) {
	for _, v := range []bool{preferUser, !preferUser} {
		if probe(v) == nil {
			return v, true
		}
	}
	return preferUser, false
}

func Installed(cfgPath string) (userService, found bool) {
	return resolveVariant(os.Geteuid() != 0, func(userService bool) error {
		s, err := newService(cfgPath, userService, nil)
		if err != nil {
			return err
		}
		_, err = s.Status()
		return err
	})
}

func Status(cfgPath string) (string, error) {
	userService, found := Installed(cfgPath)
	if !found {
		return "not installed", nil
	}
	s, err := newService(cfgPath, userService, nil)
	if err != nil {
		return "", err
	}
	st, err := s.Status()
	if err != nil {
		return "", err
	}
	switch st {
	case ksvc.StatusRunning:
		return "running", nil
	case ksvc.StatusStopped:
		return "stopped", nil
	default:
		return "unknown", nil
	}
}

func UnitCurrent(cfgPath string) (bool, error) {
	userService, found := Installed(cfgPath)
	if !found {
		return false, ErrNotInstalled
	}
	s, err := newService(cfgPath, userService, nil)
	if err != nil {
		return false, err
	}
	if !strings.HasPrefix(s.Platform(), "linux-systemd") {
		return true, nil
	}
	path := filepath.Join("/etc/systemd/system", brand.Current.ServiceName+".service")
	if userService {
		dir, err := os.UserConfigDir()
		if err != nil {
			return false, err
		}
		path = filepath.Join(dir, "systemd", "user", brand.Current.ServiceName+".service")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return strings.Contains(string(b), "\nKillMode=process\n"), nil
}
