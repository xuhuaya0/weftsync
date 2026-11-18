//go:build !windows

package console

// Hide does nothing: a terminal on Linux or macOS is where the user started the
// program on purpose, and taking it away would be the opposite of helpful.
func Hide() {}

// Errorf does nothing: the terminal is visible, so the caller's error message
// reaches the user the ordinary way.
func Errorf(title, format string, args ...any) {}
