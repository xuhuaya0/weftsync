// Package autostart registers weftsync to start when the user logs in.
//
// Two rules shape this:
//
//  1. Nothing is written outside the user's own session. Autostart is a
//     per-user convenience, so it goes in HKCU on Windows and in
//     ~/.config/autostart on Linux. Elevation is never requested.
//  2. The entry is generated from the invocation that is running right now, so
//     "enable autostart" reproduces what the user has working instead of asking
//     them to retype a command line. That also means paths must be absolute:
//     a login session has no useful working directory, and a relative
//     "job.toml" would resolve somewhere else or not at all.
package autostart

import (
	"path/filepath"
	"strings"
)

// Spec is the command an autostart entry should run.
type Spec struct {
	// Exe is the absolute path of the executable.
	Exe string
	// Args are the arguments after the executable.
	Args []string
}

// Status describes the autostart entry as it exists now.
type Status struct {
	// Supported is false on platforms with no implementation; the UI must not
	// offer a toggle it cannot honour.
	Supported bool `json:"supported"`
	// Enabled means an entry exists.
	Enabled bool `json:"enabled"`
	// Location is where the entry lives, for showing the user.
	Location string `json:"location"`
	// Command is what the entry currently runs (empty when disabled).
	Command string `json:"command"`
	// Matches means the existing entry runs exactly what this process would
	// write. A stale entry (different exe or job files) is Enabled but not
	// Matches, which is what the UI warns about.
	Matches bool `json:"matches"`
	// Detail explains an unsupported or unreadable state.
	Detail string `json:"detail,omitempty"`
}

// Qualified turns a possibly relative path into an absolute one.
//
// An empty string stays empty so callers can treat "flag not given" and
// "flag given as '.'" differently.
func Qualified(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	return filepath.Abs(path)
}

// Invocation is how the current process was started.
//
// It is a struct rather than a parameter list because every field is a flag the
// autostart entry may need to reproduce, and positional booleans at a call site
// are the kind of thing that silently swaps two of them.
type Invocation struct {
	// Exe is normally os.Executable().
	Exe string
	// JobFiles are the positional arguments.
	JobFiles []string
	// Addr, LogFile, ConfigFile and the booleans mirror the flags.
	Addr       string
	LogFile    string
	ConfigFile string
	RunNow     bool
	Tray       bool
	// DefaultAddr is the built-in value of Addr; when Addr equals it, the flag
	// is not recorded.
	DefaultAddr string
}

// SpecFrom builds the entry's command from the invocation.
//
// It is shared by every platform so the entry says the same thing everywhere;
// only the quoting differs.
func SpecFrom(in Invocation) (Spec, error) {
	exe, err := filepath.Abs(in.Exe)
	if err != nil {
		return Spec{}, err
	}
	spec := Spec{Exe: exe, Args: []string{"daemon"}}

	for _, f := range in.JobFiles {
		abs, err := filepath.Abs(f)
		if err != nil {
			return Spec{}, err
		}
		spec.Args = append(spec.Args, abs)
	}
	// Only record flags that differ from the defaults: an entry that spells out
	// every default keeps working after a default changes, which is the opposite
	// of what the user wants.
	if in.Addr != "" && in.Addr != in.DefaultAddr {
		spec.Args = append(spec.Args, "--addr", in.Addr)
	}
	if in.LogFile != "" {
		abs, err := filepath.Abs(in.LogFile)
		if err != nil {
			return Spec{}, err
		}
		spec.Args = append(spec.Args, "--log-file", abs)
	}
	if in.ConfigFile != "" {
		abs, err := filepath.Abs(in.ConfigFile)
		if err != nil {
			return Spec{}, err
		}
		spec.Args = append(spec.Args, "--config", abs)
	}
	if in.RunNow {
		spec.Args = append(spec.Args, "--run-now")
	}
	// The tray is a flag rather than a default, so it has to be carried over;
	// otherwise turning on autostart quietly drops the icon the user turned on.
	if in.Tray {
		spec.Args = append(spec.Args, "--tray")
	}
	return spec, nil
}
