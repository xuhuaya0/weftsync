package feishu

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"weftsync/state"
)

// conflictFs wires an Fs over a fake tree with a snapshot of recorded tokens.
func conflictFs(t *testing.T, srv *dedupeServer, recorded map[string]string) *Fs {
	t.Helper()

	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {})
	c.listFolderFn = srv.handler(t)

	const pairID = "aa11aa11aa11aa11"
	statePath := filepath.Join(t.TempDir(), pairID+".state.json")
	snap := state.New(pairID)
	snap.SetPath(statePath)
	for path, token := range recorded {
		snap.Put(path, state.Entry{
			LocalMTime: 1700000000, LocalSize: 1, RemoteToken: token, SyncedAt: 1700000000,
		})
	}
	if err := state.Save(snap); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	loaded, err := state.Load(statePath)
	if err != nil || loaded == nil {
		t.Fatalf("reload state: %v", err)
	}

	f := &Fs{name: "test", client: c, st: loaded, stPath: statePath, storable: true}
	f.resolver = newDirResolver(c, "root-token", "", "")
	return f
}

// The three signals from PLAN.md §4.2 must each be detected.
func TestConflictsDetectsAllThreeSignals(t *testing.T) {
	srv := newDedupeServer()
	// 1. recorded token still present and alone -> no conflict.
	srv.add("root-token", "fine.txt", "tok-fine", 100)
	// 2. the recorded name is not in the folder at all.
	//    (deleted.txt is deliberately absent.)
	// 3. name now carries a different token from the recorded one.
	srv.addDir("root-token", "sub", "dir-1")
	srv.add("dir-1", "replaced.txt", "tok-new", 200)
	// 4. two entries under one name.
	srv.add("root-token", "dupe.txt", "tok-dup1", 100)
	srv.add("root-token", "dupe.txt", "tok-dup2", 200)

	f := conflictFs(t, srv, map[string]string{
		"fine.txt":         "tok-fine",
		"deleted.txt":      "tok-deleted-forever",
		"sub/replaced.txt": "tok-old",
		"dupe.txt":         "tok-dup1",
	})

	rep, err := f.conflictsCommand(context.Background(), "")
	if err != nil {
		t.Fatalf("conflicts: %v", err)
	}

	got := map[string]string{} // path -> kind
	for _, c := range rep.Conflicts {
		p := c.Name
		if c.Dir != "" {
			p = c.Dir + "/" + c.Name
		}
		got[p] = c.Kind
	}

	want := map[string]string{
		"deleted.txt":      ConflictRemoteDeleted,
		"sub/replaced.txt": ConflictRemoteReplaced,
		"dupe.txt":         ConflictDuplicateName,
	}
	for p, kind := range want {
		if got[p] != kind {
			t.Errorf("%s: kind = %q, want %q (all: %v)", p, got[p], kind, got)
		}
	}
	if _, ok := got["fine.txt"]; ok {
		t.Error("an unchanged file must not be reported as a conflict")
	}
	if len(rep.Conflicts) != len(want) {
		t.Errorf("reported %d conflicts, want %d: %+v", len(rep.Conflicts), len(want), rep.Conflicts)
	}
	if rep.Checked != 4 {
		t.Errorf("checked = %d, want 4", rep.Checked)
	}
}

// Detection must refuse rather than pretend there is nothing to see when it has
// no baseline: "no conflicts" and "cannot tell" are different answers.
func TestConflictsRequiresState(t *testing.T) {
	srv := newDedupeServer()
	srv.add("root-token", "a.txt", "tok", 100)

	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {})
	c.listFolderFn = srv.handler(t)
	f := &Fs{name: "test", client: c}
	f.resolver = newDirResolver(c, "root-token", "", "")

	if _, err := f.conflictsCommand(context.Background(), ""); err == nil {
		t.Fatal("expected an error when there is no snapshot to compare against")
	}
}

// Duplicates at paths the snapshot never recorded still matter: a sync would pick
// one of them silently.
func TestConflictsDetectsUnrecordedDuplicates(t *testing.T) {
	srv := newDedupeServer()
	srv.add("root-token", "stranger.txt", "tok-1", 100)
	srv.add("root-token", "stranger.txt", "tok-2", 200)

	f := conflictFs(t, srv, nil)

	rep, err := f.conflictsCommand(context.Background(), "")
	if err != nil {
		t.Fatalf("conflicts: %v", err)
	}
	if len(rep.Conflicts) != 1 {
		t.Fatalf("got %d conflicts, want 1: %+v", len(rep.Conflicts), rep.Conflicts)
	}
	c := rep.Conflicts[0]
	if c.Kind != ConflictDuplicateName || c.Name != "stranger.txt" {
		t.Errorf("conflict = %+v", c)
	}
	if len(c.Found) != 2 {
		t.Errorf("found = %v, want both tokens", c.Found)
	}
}

// A clean pair reports nothing, so the caller can distinguish "fine" from "found
// problems".
func TestConflictsCleanTree(t *testing.T) {
	srv := newDedupeServer()
	srv.add("root-token", "a.txt", "tok-a", 100)
	srv.addDir("root-token", "d", "dir-1")
	srv.add("dir-1", "b.txt", "tok-b", 100)

	f := conflictFs(t, srv, map[string]string{
		"a.txt":   "tok-a",
		"d/b.txt": "tok-b",
	})

	rep, err := f.conflictsCommand(context.Background(), "")
	if err != nil {
		t.Fatalf("conflicts: %v", err)
	}
	if len(rep.Conflicts) != 0 {
		t.Errorf("expected no conflicts, got %+v", rep.Conflicts)
	}
	if rep.Checked != 2 {
		t.Errorf("checked = %d, want 2", rep.Checked)
	}
}
