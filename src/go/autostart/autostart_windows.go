//go:build windows

package autostart

import (
	"errors"
	"fmt"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

// Windows keeps per-user login startup commands in one registry value each.
// HKCU is deliberate: this must not ask for elevation, and one user's autostart
// has no business affecting another's.
//
// These are variables rather than constants only so tests can point at a
// throwaway key: a test must never edit the real autostart list of the machine
// it runs on.
var (
	runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue   = "weftsync"
)

// Supported reports whether autostart can be managed here.
func Supported() bool { return true }

// Location is where the entry lives, in a form a user can look up.
func Location() string {
	return `HKCU\` + runKeyPath + ` → ` + runValue
}

// Command renders the entry the way the Run key expects it.
//
// syscall.EscapeArg implements the same quoting CreateProcess uses, so a path
// with spaces survives. Writing an unquoted path here is the classic way to get
// an entry that silently fails to start after the user moves the binary into
// "C:\Program Files".
func Command(spec Spec) string {
	parts := make([]string, 0, len(spec.Args)+1)
	parts = append(parts, syscall.EscapeArg(spec.Exe))
	for _, a := range spec.Args {
		parts = append(parts, syscall.EscapeArg(a))
	}
	return strings.Join(parts, " ")
}

// Read inspects the current entry and compares it with want.
func Read(want Spec) (Status, error) {
	st := Status{Supported: true, Location: Location()}

	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("open Run key: %w", err)
	}
	defer func() { _ = k.Close() }()

	cur, _, err := k.GetStringValue(runValue)
	if errors.Is(err, registry.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("read %s: %w", runValue, err)
	}

	st.Enabled = true
	st.Command = cur
	if want.Exe != "" {
		st.Matches = strings.TrimSpace(cur) == strings.TrimSpace(Command(want))
	}
	return st, nil
}

// Enable writes the entry, replacing any previous one.
func Enable(spec Spec) error {
	if strings.TrimSpace(spec.Exe) == "" {
		return errors.New("autostart: the executable path is empty")
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open Run key for writing: %w", err)
	}
	defer func() { _ = k.Close() }()

	if err := k.SetStringValue(runValue, Command(spec)); err != nil {
		return fmt.Errorf("write %s: %w", runValue, err)
	}
	return nil
}

// Disable removes the entry. Removing something that is not there is not an
// error: the caller asked for "not enabled", and that is the state afterwards.
func Disable() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open Run key for writing: %w", err)
	}
	defer func() { _ = k.Close() }()

	if err := k.DeleteValue(runValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("delete %s: %w", runValue, err)
	}
	return nil
}
