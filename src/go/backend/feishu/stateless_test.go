package feishu

import (
	"context"
	"strings"
	"testing"
	"time"

	"weftsync/state"
)

// Without a state_file there is no snapshot, so the object can report neither the
// size nor the original local mtime it had when uploaded.
//
// This is the whole reason a stateless pair re-uploads everything on every run:
// ModTime falls back to the upload moment, which differs from the local file's
// mtime on every pass, and Size falls back to -1 ("not comparable"). Measured on
// the real tenant: a 1487-file pair reported 1487 updates and 116 MB to transfer
// on a run where nothing had changed.
//
// The backend's own state_file option used to advertise "leave empty to derive it
// from the local and remote roots, which is the normal case" while no such
// derivation existed anywhere. Anyone reading that help would have concluded that
// leaving it empty was safe.
func TestStatelessObjectCannotProveUnchanged(t *testing.T) {
	// Same fixture the other update tests use, minus the snapshot.
	c, _ := testClient(t, jsonBody(t, map[string]any{
		"code": 0,
		"data": map[string]any{"files": []any{}, "has_more": false},
	}))
	f := &Fs{name: "test", client: c, storable: false}
	f.resolver = newDirResolver(c, "root-token", "", "")

	// A remote entry as the listing would report it: modified_time is the upload
	// moment, and there is no size field at all.
	uploaded := time.Date(2026, 11, 13, 14, 18, 5, 0, time.UTC).Unix()
	o := &Object{
		fs:     f,
		remote: "a/b.xlsx",
		entry:  remoteEntry{Name: "b.xlsx", Token: "tok", ModTime: uploaded},
	}

	if got := o.Size(); got != -1 {
		t.Errorf("Size() = %d, want -1 (unknown); Feishu's listing has no size", got)
	}

	// The local file was last written long before the upload.
	localMTime := time.Date(2025, 5, 24, 10, 29, 47, 0, time.UTC)
	if got := o.ModTime(context.Background()); got.Equal(localMTime) {
		t.Fatalf("ModTime() = the local mtime, but there is no snapshot to know it")
	}
	if got := o.ModTime(context.Background()); !got.Equal(time.Unix(uploaded, 0)) {
		t.Errorf("ModTime() = %v, want the upload moment %v", got, time.Unix(uploaded, 0))
	}

	// And that is exactly why rclone sees a change: size is "not comparable" and
	// the times differ, so the pair is not equal.
	if o.ModTime(context.Background()).Equal(localMTime) {
		t.Error("a stateless pair must not look unchanged")
	}

	// Put the same file in a snapshot and the same two calls answer correctly.
	f.st = state.New("0123456789abcdef")
	f.st.Put("a/b.xlsx", state.Entry{
		LocalMTime: localMTime.Unix(), LocalSize: 78424, RemoteToken: "tok",
	})
	if got := o.Size(); got != 78424 {
		t.Errorf("with a snapshot Size() = %d, want 78424", got)
	}
	if got := o.ModTime(context.Background()); !got.Equal(localMTime) {
		t.Errorf("with a snapshot ModTime() = %v, want %v", got, localMTime)
	}
}

// The help text must not promise behaviour the code does not have.
//
// This is not pedantry: the removed sentence said leaving state_file empty was
// "the normal case", which is the opposite of what happens.
func TestStateFileHelpDoesNotPromiseDerivation(t *testing.T) {
	var help string
	for _, opt := range backendOptions() {
		if opt.Name == "state_file" {
			help = opt.Help
			break
		}
	}
	if help == "" {
		t.Fatal("the state_file option is gone; the doc below no longer applies")
	}
	for _, forbidden := range []string{"derive it", "derived from", "the normal case"} {
		if strings.Contains(help, forbidden) {
			t.Errorf("help still claims derivation (%q):\n%s", forbidden, help)
		}
	}
	for _, want := range []string{"no default", "re-uploads every file"} {
		if !strings.Contains(help, want) {
			t.Errorf("help does not state %q:\n%s", want, help)
		}
	}
}
