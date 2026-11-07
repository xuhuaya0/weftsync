// Package tray puts an icon in the notification area (Windows) so the daemon is
// reachable without a console window.
//
// Design rules:
//
//  1. A tray that fails must never take the daemon down. Everything here returns
//     an error and the caller keeps running without an icon; the web UI is the
//     primary interface and the tray is a shortcut to it.
//  2. The icon is drawn at runtime instead of shipped as a file, so the binary
//     stays a single file with nothing to install next to it.
//  3. No cgo. Everything goes through the Win32 API directly, which keeps
//     cross-compiling from Linux working.
package tray

import "errors"

// ErrUnsupported means this platform has no notification area implementation.
var ErrUnsupported = errors.New("tray: not supported on this platform")

// Config is what the icon offers when clicked.
type Config struct {
	// Tooltip is shown on hover.
	Tooltip string
	// ConsoleURL is opened in the default browser on double-click.
	ConsoleURL string
	// OnRunAll is invoked by the "run everything" menu item. It runs on the
	// tray's message thread, so it must not block: the daemon's run is async.
	OnRunAll func()
	// OnQuit is invoked by the "quit" menu item.
	OnQuit func()
	// OnError receives failures that happen after Start returned, such as the
	// message loop dying. Without it those would be invisible.
	OnError func(error)
}
