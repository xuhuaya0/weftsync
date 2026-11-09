package feishu

import (
	"testing"

	"weftsync/split"
)

// The shared pre-flight check finds this backend's limits by asking, so losing
// the methods must break the build rather than silently disable the check: a
// Feishu pair without it would upload into a directory until the API refused
// mid-run.
var _ split.LimitReporter = (*Fs)(nil)

// TestDeclaredLimits pins the numbers to the documentation they came from.
//
// They are documented, not measured: no run has reached either limit. The largest
// directory in the real data set holds 6,399 files, which is refused locally long
// before an upload could test the boundary.
func TestDeclaredLimits(t *testing.T) {
	f := &Fs{}

	// create_folder "Limits" and error 1062507: "1,500 nodes per level".
	if got := f.MaxChildrenPerDir(); got != 1500 {
		t.Errorf("MaxChildrenPerDir = %d, want 1500", got)
	}
	// Error 1062506: "up to 15 levels of directories".
	if got := f.MaxDirDepth(); got != 15 {
		t.Errorf("MaxDirDepth = %d, want 15", got)
	}

	// And the threshold we actually refuse at is the documented limit minus the
	// headroom, which is what the user-facing message quotes.
	if got := split.RefuseAtFor(f.MaxChildrenPerDir()); got != 1400 {
		t.Errorf("RefuseAtFor(1500) = %d, want 1400", got)
	}
}
