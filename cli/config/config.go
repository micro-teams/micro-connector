// Package config is the installation's identity on disk: where the control plane is and the
// machine's credential. Every process on the machine (the service and any command a person runs)
// reads the same file, so the machine is configured once.
//
//	Config{Base, Token, MachineID, WS}  Base is the control plane's origin; Token the machine's
//	                                    credential from enrolment; WS an explicit link URL, derived
//	                                    from Base when empty.
//	DefaultPath() string   <user config dir>/<brand ConfigDir>/config.json; panics without a user
//	                       config dir (no $HOME), since nothing could be configured then.
//	Dir() string           the directory of DefaultPath.
//	Load(path) (*Config, error); Save(path, *Config) error (directory 0700, file 0600).
//	(*Config).APIBase() string            Base without a trailing slash.
//	(*Config).ControlURL() (string, error) WS, or Base with a ws/wss scheme and /machine/link.
//
// Example:
//
//	cfg, err := config.Load(config.DefaultPath())
//	url, err := cfg.ControlURL()
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/micro-teams/micro-connector/cli/brand"
)

type Config struct {
	Base      string `json:"base"`
	Token     string `json:"token"`
	MachineID string `json:"machine_id,omitempty"`
	WS        string `json:"ws,omitempty"`
}

func DefaultPath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		panic(fmt.Errorf("config: %w", err))
	}
	return filepath.Join(base, brand.Current.ConfigDir, "config.json")
}

func Dir() string { return filepath.Dir(DefaultPath()) }

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return &c, nil
}

func Save(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (c *Config) APIBase() string { return strings.TrimRight(c.Base, "/") }

func (c *Config) ControlURL() (string, error) {
	if c.WS != "" {
		return c.WS, nil
	}
	u, err := url.Parse(c.Base)
	if err != nil {
		return "", fmt.Errorf("config: bad base %q: %w", c.Base, err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http", "":
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/machine/link"
	return u.String(), nil
}
