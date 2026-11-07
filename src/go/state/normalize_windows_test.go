//go:build windows

package state

import "testing"

// On Windows a backslash can only be a separator, so it is normalised to "/".
// (Linux has its own file: there a backslash is a legal filename character and
// must survive.)
func TestNormalizePathWindows(t *testing.T) {
	f := New("p")
	f.Put(`a\b\c.txt`, Entry{LocalMTime: 1, LocalSize: 1, RemoteToken: "T"})
	if _, ok := f.Lookup("a/b/c.txt"); !ok {
		t.Error(`backslash path was not normalised to "a/b/c.txt"`)
	}
}
