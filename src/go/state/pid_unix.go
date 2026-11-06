//go:build !windows

package state

import (
	"errors"
	"syscall"
)

// pidAlive reports whether a process with the given pid is still running.
//
// Signal 0 performs the permission and existence checks without delivering
// anything. ESRCH means the process is gone; EPERM means it exists but belongs
// to another user, which still counts as alive.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}
