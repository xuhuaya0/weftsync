//go:build !windows && !linux

package autostart

import "errors"

// Only Windows and Linux have an implementation. The UI asks Supported before
// showing the toggle, so this reports the gap instead of pretending to work.

// Supported reports whether autostart can be managed here.
func Supported() bool { return false }

// Location is empty when there is nowhere to write.
func Location() string { return "" }

// Command renders the entry; nothing consumes it on these platforms.
func Command(spec Spec) string { return "" }

// Read reports the feature as unavailable.
func Read(want Spec) (Status, error) {
	return Status{
		Supported: false,
		Detail:    "autostart is only implemented on Windows and Linux",
	}, nil
}

// Enable always fails on unsupported platforms.
func Enable(spec Spec) error {
	return errors.New("autostart is only implemented on Windows and Linux")
}

// Disable always fails on unsupported platforms.
func Disable() error {
	return errors.New("autostart is only implemented on Windows and Linux")
}
