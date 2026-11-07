//go:build !windows

package state

import "testing"

// On Linux a backslash is an ordinary filename character, so normalisation must
// leave it alone. An unconditional replace turned a file literally named
// "weird\name.txt" into "weird/name.txt" - a different path, uploaded separately.
func TestNormalizePathKeepsBackslashOnUnix(t *testing.T) {
	cases := []struct{ in, want string }{
		{`weird\name.txt`, `weird\name.txt`},
		{`dir/weird\name.txt`, `dir/weird\name.txt`},
		{`/leading/`, `leading`},
		{`a//b`, `a/b`},
	}
	for _, c := range cases {
		if got := NormalizePath(c.in); got != c.want {
			t.Errorf("NormalizePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
