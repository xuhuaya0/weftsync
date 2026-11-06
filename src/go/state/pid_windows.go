//go:build windows

package state

import "syscall"

// pidAlive reports whether a process with the given pid is still running.
//
// Used to decide whether a lock file is stale. os.FindProcess is not usable
// here: on Windows it succeeds for any pid, which would make every stale lock
// look alive. Instead we ask for a handle with the minimum rights needed.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = syscall.CloseHandle(h) }()

	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	const stillActive = 259 // STILL_ACTIVE
	return code == stillActive
}
