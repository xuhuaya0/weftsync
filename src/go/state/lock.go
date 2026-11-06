package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Lock is an advisory exclusive lock on one pair's state file.
//
// Only one process may sync a given pair at a time; two concurrent runs would
// each believe they own the remote state and could delete each other's files.
// Within one process the lock is shared and reference counted, because rclone
// legitimately builds several Fs instances for the same remote.
type Lock struct {
	path string
	refs int
}

// ErrLocked means another process owns the pair right now.
var ErrLocked = errors.New("state: another weftsync process is syncing this pair")

var (
	lockMu sync.Mutex
	held   = make(map[string]*Lock)
)

// AcquireLock takes the lock for statePath.
//
// The lock file records the owning PID and the acquisition time. A lock whose
// PID is no longer running is considered stale and is taken over, so a killed
// process does not block the pair forever.
//
// Liveness — not elapsed time — decides staleness. A large first sync can
// legitimately run for hours, so a "stale after N minutes" rule would let a
// second process start syncing and delete files underneath the first.
func AcquireLock(statePath string) (*Lock, error) {
	lockPath := LockPath(statePath)

	lockMu.Lock()
	defer lockMu.Unlock()

	if l, ok := held[lockPath]; ok {
		l.refs++
		return l, nil
	}

	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("state: create lock directory: %w", err)
	}

	payload := fmt.Sprintf("%d\n%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))

	// O_EXCL makes creation atomic: exactly one process wins.
	fh, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		if _, werr := fh.WriteString(payload); werr != nil {
			_ = fh.Close()
			_ = os.Remove(lockPath)
			return nil, fmt.Errorf("state: write lock file: %w", werr)
		}
		if cerr := fh.Close(); cerr != nil {
			_ = os.Remove(lockPath)
			return nil, fmt.Errorf("state: close lock file: %w", cerr)
		}
		l := &Lock{path: lockPath, refs: 1}
		held[lockPath] = l
		return l, nil
	}

	if !os.IsExist(err) {
		return nil, fmt.Errorf("state: create lock file: %w", err)
	}

	// Someone holds it. Decide whether that someone still exists.
	pid, when, ok := readLock(lockPath)
	if !ok {
		// Unreadable/garbage lock: treat as stale rather than blocking forever.
		return takeOver(lockPath, payload, "unreadable lock content")
	}
	if pidAlive(pid) {
		return nil, fmt.Errorf("%w (pid %d, since %s)", ErrLocked, pid, when)
	}
	return takeOver(lockPath, payload, fmt.Sprintf("pid %d is not running", pid))
}

// LockPath returns where the lock for a state file lives.
func LockPath(statePath string) string {
	return strings.TrimSuffix(statePath, filepath.Ext(statePath)) + ".lock"
}

func takeOver(lockPath, payload, reason string) (*Lock, error) {
	fmt.Fprintf(os.Stderr, "WARNING: taking over stale lock %s (%s)\n", lockPath, reason)
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("state: remove stale lock: %w", err)
	}
	fh, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("state: recreate lock file: %w", err)
	}
	if _, err := fh.WriteString(payload); err != nil {
		_ = fh.Close()
		return nil, fmt.Errorf("state: write lock file: %w", err)
	}
	if err := fh.Close(); err != nil {
		return nil, fmt.Errorf("state: close lock file: %w", err)
	}
	l := &Lock{path: lockPath, refs: 1}
	held[lockPath] = l
	return l, nil
}

func readLock(path string) (pid int, when string, ok bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, "", false
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 {
		return 0, "", false
	}
	pid, err = strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return 0, "", false
	}
	if len(lines) > 1 {
		when = strings.TrimSpace(lines[1])
	}
	return pid, when, true
}

// Release drops one reference; the lock file is removed when the last one goes.
func (l *Lock) Release() {
	if l == nil {
		return
	}
	lockMu.Lock()
	defer lockMu.Unlock()

	if l.refs > 1 {
		l.refs--
		return
	}
	_ = os.Remove(l.path)
	delete(held, l.path)
	l.refs = 0
}

// Path exposes the lock file location (used by tests and diagnostics).
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}
