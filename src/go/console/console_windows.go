//go:build windows

// Package console controls the console window the daemon was started with.
//
// It exists for one case: a double-clicked executable. Windows creates a console
// window for it, and that window is the only thing the user sees - it closes the
// instant the process exits, which is what a crash looks like. For a program whose
// interface is a web page the window is noise, so it is hidden once the server is
// answering.
package console

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")

	procGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	procShowWindow       = user32.NewProc("ShowWindow")
	procMessageBox       = user32.NewProc("MessageBoxW")
)

const (
	swHide = 0
	// mbOK / mbIconError: a single OK button and the error icon. Enough for "this
	// did not start", which is all this is ever used for.
	mbOK        = 0x00000000
	mbIconError = 0x00000010
)

// Hide removes the console window from view, keeping the console itself.
//
// FreeConsole would detach it entirely, after which writes to stdout fail; hiding
// keeps the log sink working for anything that still writes there. A process with
// no console (started from a service, or already detached) is not an error: there
// is nothing to hide.
func Hide() {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return
	}
	_, _, _ = procShowWindow.Call(hwnd, swHide)
}

// Errorf shows a modal message box.
//
// Needed exactly where the console is not: a double-clicked process that fails
// before its window could be read leaves the user with nothing at all. Called only
// on startup failures, never in a loop - a message box blocks.
func Errorf(title, format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	t, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	b, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	_, _, _ = procMessageBox.Call(0, uintptr(unsafe.Pointer(b)), uintptr(unsafe.Pointer(t)),
		mbOK|mbIconError)
}
