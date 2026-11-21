// Package brand holds the names that make a connector one product rather than another: the
// binary's name, the prefix of its environment variables, where its config and runtime files live,
// the name of its service, and the control plane paths it enrols against and updates from.
// Everything else in this library is the same whoever's product it is, so these names are the only
// place a product appears.
//
// One brand per process: a product sets Current at startup, before anything reads a path or an
// environment variable, and never changes it.
//
//	Brand                   the names (fields documented below).
//	Current                 the brand of this process; the default is a deliberately generic one.
//	(Brand).Env(name)       the full variable name: Env("TOKEN") = "<EnvPrefix>_TOKEN".
//	(Brand).Getenv(name)    os.Getenv(Env(name)).
//	(Brand).RuntimePath()   ~/.local/state/<RuntimeDir>: the private tmux socket and screen temp dirs.
//	                        The same for every process of the user (the service and a login shell
//	                        meet there), stable across restarts and updates, and not in /tmp, where
//	                        any user can take the name first and a reboot empties it. Without a home
//	                        directory it is <temp>/<RuntimeDir>-<uid>.
//	(Brand).XDGRuntimePath(), (Brand).LegacyRuntimePath()
//	                        the two places older connectors kept the socket ($XDG_RUNTIME_DIR/<RuntimeDir>
//	                        and <temp>/<RuntimeDir>-<uid>); terminal.NewManager joins a server still
//	                        answering there so an update does not orphan its screens.
//
// Example:
//
//	brand.Current = brand.Brand{Name: "yourthing", EnvPrefix: "YOURTHING", ConfigDir: "yourthing",
//		RuntimeDir: "yourthing", ServiceName: "yourthing", EnrollBase: "/machine/enroll",
//		BinaryBase: "/connector/latest"}
//	token := brand.Current.Getenv("TOKEN")
package brand

import (
	"os"
	"path/filepath"
	"strconv"
)

type Brand struct {
	// Name is the binary's name, and the prefix on everything it says to a terminal.
	Name string
	// EnvPrefix prefixes the environment variables this connector reads and injects into screens
	// (e.g. "MICROTEAMS" -> MICROTEAMS_API, MICROTEAMS_TOKEN, MICROTEAMS_SCREEN).
	EnvPrefix string
	// ConfigDir is the directory under the user's config home holding config.json.
	ConfigDir string
	// RuntimeDir names the per-user runtime directory that holds the private tmux socket. Products
	// must not share one, or each would see and could end the other's screens.
	RuntimeDir string
	// ServiceName is the service's name. Changing it orphans installed services: acting on the new
	// name does nothing to the old one.
	ServiceName string
	// ServiceDisplayName and ServiceDescription are what a person reads in their init system.
	ServiceDisplayName string
	ServiceDescription string
	// EnrollBase is the control plane's enrollment path, e.g. "/machine/enroll", under which
	// "/start" and "/poll" live.
	EnrollBase string
	// BinaryBase is where a published binary lives on the control plane's origin, e.g.
	// "/connector/latest", followed by "/<os>-<arch>/<Name>".
	BinaryBase string
}

// Current is this process's brand. The default belongs to no product, so a connector that forgot
// to set it is obviously unconfigured instead of using another product's socket and config.
var Current = Brand{
	Name:               "connector",
	EnvPrefix:          "CONNECTOR",
	ConfigDir:          "connector",
	RuntimeDir:         "connector",
	ServiceName:        "connector",
	ServiceDisplayName: "Connector",
	ServiceDescription: "Connects this machine to its control plane.",
	EnrollBase:         "/machine/enroll",
	BinaryBase:         "/connector/latest",
}

func (b Brand) Env(name string) string { return b.EnvPrefix + "_" + name }

func (b Brand) Getenv(name string) string { return os.Getenv(b.Env(name)) }

func (b Brand) RuntimePath() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "state", b.RuntimeDir)
	}
	return b.LegacyRuntimePath()
}

func (b Brand) XDGRuntimePath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, b.RuntimeDir)
	}
	return ""
}

func (b Brand) LegacyRuntimePath() string {
	return filepath.Join(os.TempDir(), b.RuntimeDir+"-"+strconv.Itoa(os.Getuid()))
}
