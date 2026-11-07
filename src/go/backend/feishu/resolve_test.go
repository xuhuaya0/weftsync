package feishu

import (
	"context"
	"strings"
	"testing"

	"weftsync/state"
)

// forget must drop the record so the next run uploads over the remote.
func TestResolveForgetDropsRecord(t *testing.T) {
	srv := newDedupeServer()
	srv.add("root-token", "a.txt", "tok-a", 100)
	f := conflictFs(t, srv, map[string]string{"a.txt": "tok-a"})

	if _, err := f.st.Lookup("a.txt"); !err {
		t.Fatal("the fixture should have a record to drop")
	}

	rep, err := f.resolveCommand(context.Background(), nil,
		map[string]string{"action": "forget", "path": "a.txt"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !rep.Changed {
		t.Errorf("Changed = false, want true\n%s", rep.Summary)
	}
	if _, ok := f.st.Lookup("a.txt"); ok {
		t.Error("the record survived forget")
	}

	// The change must be on disk, not only in memory: the daemon's next run loads
	// the file.
	reloaded, err := state.Load(f.stPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded == nil {
		t.Fatal("the state file disappeared")
	}
	if _, ok := reloaded.Lookup("a.txt"); ok {
		t.Error("the record is still in the saved state file")
	}
}

// forget on a path with no record must say nothing changed, so the caller does
// not report success for an edit that did not happen.
func TestResolveForgetWithoutRecordReportsUnchanged(t *testing.T) {
	srv := newDedupeServer()
	srv.add("root-token", "a.txt", "tok-a", 100)
	f := conflictFs(t, srv, map[string]string{})

	rep, err := f.resolveCommand(context.Background(), nil,
		map[string]string{"action": "forget", "path": "a.txt"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if rep.Changed {
		t.Error("Changed = true although there was no record to drop")
	}
	if rep.Summary == "" {
		t.Error("the caller needs a sentence to show")
	}
}

// adopt records the token the folder currently carries.
func TestResolveAdoptRecordsToken(t *testing.T) {
	srv := newDedupeServer()
	srv.add("root-token", "a.txt", "tok-current", 4321)
	f := conflictFs(t, srv, map[string]string{"a.txt": "tok-old"})

	rep, err := f.resolveCommand(context.Background(), nil,
		map[string]string{
			"action": "adopt", "path": "a.txt", "token": "tok-current",
			"mtime": "1700000123", "size": "99",
		})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !rep.Changed {
		t.Errorf("Changed = false\n%s", rep.Summary)
	}

	e, ok := f.st.Lookup("a.txt")
	if !ok {
		t.Fatal("no record after adopt")
	}
	if e.RemoteToken != "tok-current" || e.LocalMTime != 1700000123 || e.LocalSize != 99 {
		t.Errorf("record = %+v", e)
	}

	// And the conflict must be gone now: that is the whole point.
	scan, err := f.conflictsCommand(context.Background(), "")
	if err != nil {
		t.Fatalf("conflicts: %v", err)
	}
	if len(scan.Conflicts) != 0 {
		t.Errorf("the conflict is still reported after adopt: %+v", scan.Conflicts)
	}
}

// Adopting a token the folder does not carry would leave the snapshot disagreeing
// with reality, and the conflict would come straight back.
func TestResolveAdoptRefusesUnknownToken(t *testing.T) {
	srv := newDedupeServer()
	srv.add("root-token", "a.txt", "tok-current", 100)
	f := conflictFs(t, srv, map[string]string{"a.txt": "tok-current"})

	_, err := f.resolveCommand(context.Background(), nil,
		map[string]string{
			"action": "adopt", "path": "a.txt", "token": "tok-from-a-stale-screen",
			"mtime": "1700000123", "size": "1",
		})
	if err == nil {
		t.Fatal("adopting a token that is not there must be refused")
	}
	if !strings.Contains(err.Error(), "not at") {
		t.Errorf("the error should say the token is gone, got: %v", err)
	}
}

// Missing parameters are refused rather than guessed.
func TestResolveRejectsBadInput(t *testing.T) {
	srv := newDedupeServer()
	srv.add("root-token", "a.txt", "tok-a", 100)
	f := conflictFs(t, srv, map[string]string{"a.txt": "tok-a"})
	ctx := context.Background()

	cases := []struct {
		name string
		opt  map[string]string
	}{
		{"no action", map[string]string{"path": "a.txt"}},
		{"unknown action", map[string]string{"action": "delete", "path": "a.txt"}},
		{"no path", map[string]string{"action": "forget"}},
		{"adopt without token", map[string]string{
			"action": "adopt", "path": "a.txt", "mtime": "1", "size": "1"}},
		{"adopt without mtime", map[string]string{
			"action": "adopt", "path": "a.txt", "token": "tok-a", "size": "1"}},
		{"adopt without size", map[string]string{
			"action": "adopt", "path": "a.txt", "token": "tok-a", "mtime": "1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := f.resolveCommand(ctx, nil, c.opt); err == nil {
				t.Error("expected a refusal")
			}
		})
	}
}

// With no snapshot there is nothing to edit; saying so is better than silently
// doing nothing.
func TestResolveRefusesWithoutState(t *testing.T) {
	srv := newDedupeServer()
	c, _ := testClient(t, nil)
	c.listFolderFn = srv.handler(t)
	f := &Fs{name: "test", client: c}

	if _, err := f.resolveCommand(context.Background(), nil,
		map[string]string{"action": "forget", "path": "a.txt"}); err == nil {
		t.Fatal("resolve without a state_file must be refused")
	}
}

// A rebuilt snapshot is not evidence of anything, so editing it would record a
// guess.
func TestResolveRefusesInRepairMode(t *testing.T) {
	srv := newDedupeServer()
	srv.add("root-token", "a.txt", "tok-a", 100)
	f := conflictFs(t, srv, map[string]string{"a.txt": "tok-a"})
	f.repair = true

	if _, err := f.resolveCommand(context.Background(), nil,
		map[string]string{"action": "forget", "path": "a.txt"}); err == nil {
		t.Fatal("resolve in repair mode must be refused")
	}
}

// The path is normalised the same way the rest of the backend does it.
func TestResolveNormalisesPath(t *testing.T) {
	srv := newDedupeServer()
	srv.add("root-token", "a.txt", "tok-a", 100)
	f := conflictFs(t, srv, map[string]string{"a.txt": "tok-a"})

	rep, err := f.resolveCommand(context.Background(), nil,
		map[string]string{"action": "forget", "path": "/a.txt"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if rep.Path != "a.txt" {
		t.Errorf("path = %q, want a.txt", rep.Path)
	}
	if !rep.Changed {
		t.Error("the record should have been dropped through the normalised path")
	}
}
