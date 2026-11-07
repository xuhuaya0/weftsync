package feishu

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"weftsync/state"
)

// dedupeServer models a folder that may legitimately hold several same-named
// files, which is what Feishu does and what the collapse in List hides.
type dedupeServer struct {
	mu      sync.Mutex
	folders map[string][]remoteEntry // folder token -> raw children
	deleted []string
}

func newDedupeServer() *dedupeServer {
	return &dedupeServer{folders: map[string][]remoteEntry{}}
}

func (s *dedupeServer) add(folder, name, token string, modTime int64) {
	s.folders[folder] = append(s.folders[folder], remoteEntry{
		Name: name, Token: token, ModTime: modTime, CreatedTime: modTime,
		RawType: "file", ParentToken: folder,
	})
}

func (s *dedupeServer) addDir(folder, name, token string) {
	s.folders[folder] = append(s.folders[folder], remoteEntry{
		Name: name, Token: token, IsDir: true, RawType: "folder", ParentToken: folder,
	})
	s.folders[token] = nil
}

func (s *dedupeServer) handler(t *testing.T) func(ctx context.Context, token string) ([]remoteEntry, error) {
	t.Helper()
	return func(ctx context.Context, token string) ([]remoteEntry, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		entries, ok := s.folders[token]
		if !ok {
			return nil, &APIError{Code: CodeParentNotFound, Msg: "no such folder", Where: "test"}
		}
		out := make([]remoteEntry, len(entries))
		copy(out, entries)
		return out, nil
	}
}

// dedupeFs wires an Fs over the fake folder tree with a snapshot seeded from
// recorded (path -> token) pairs.
func dedupeFs(t *testing.T, srv *dedupeServer, recorded map[string]string) (*Fs, string) {
	t.Helper()

	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			tok := filepath.Base(r.URL.Path)
			srv.mu.Lock()
			srv.deleted = append(srv.deleted, tok)
			srv.mu.Unlock()
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	})
	// listFolder is what findDuplicates uses; point it at the model.
	c.listFolderFn = srv.handler(t)

	const pairID = "1234123412341234"
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
	return f, statePath
}

// findDuplicates must see the copies that List hides.
func TestFindDuplicatesSeesCollapsedEntries(t *testing.T) {
	srv := newDedupeServer()
	srv.add("root-token", "a.txt", "tok-a1", 100)
	srv.add("root-token", "a.txt", "tok-a2", 200)
	srv.add("root-token", "b.txt", "tok-b1", 100)
	srv.addDir("root-token", "sub", "dir-1")
	srv.add("dir-1", "c.txt", "tok-c1", 100)
	srv.add("dir-1", "c.txt", "tok-c2", 150)

	f, _ := dedupeFs(t, srv, nil)

	groups, err := f.findDuplicates(context.Background(), "")
	if err != nil {
		t.Fatalf("findDuplicates: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("found %d duplicate groups, want 2: %+v", len(groups), groups)
	}
	// Sorted by dir then name: root's a.txt first, then sub/c.txt.
	if groups[0].Name != "a.txt" || groups[0].Dir != "" {
		t.Errorf("group 0 = %q/%q", groups[0].Dir, groups[0].Name)
	}
	if groups[1].Name != "c.txt" || groups[1].Dir != "sub" {
		t.Errorf("group 1 = %q/%q", groups[1].Dir, groups[1].Name)
	}
	if len(groups[0].Entries) != 2 || len(groups[1].Entries) != 2 {
		t.Errorf("entry counts = %d, %d", len(groups[0].Entries), len(groups[1].Entries))
	}
}

// The recorded copy is kept; the extras are deleted, and only with confirm=true.
func TestDedupeKeepsRecordedCopy(t *testing.T) {
	srv := newDedupeServer()
	srv.add("root-token", "a.txt", "tok-keep", 100)
	srv.add("root-token", "a.txt", "tok-extra1", 200)
	srv.add("root-token", "a.txt", "tok-extra2", 300)

	f, _ := dedupeFs(t, srv, map[string]string{"a.txt": "tok-keep"})

	rep, err := f.dedupeCommand(context.Background(), "", map[string]string{"confirm": "true"})
	if err != nil {
		t.Fatalf("dedupe: %v", err)
	}
	if len(rep.Deleted) != 2 {
		t.Fatalf("deleted %v, want the two extras", rep.Deleted)
	}
	for _, tok := range rep.Deleted {
		if tok == "tok-keep" {
			t.Fatal("the recorded copy must never be deleted")
		}
	}
	// The kept entry must be marked so the report is unambiguous.
	var kept int
	for _, e := range rep.Groups[0].Entries {
		if e.Keep {
			kept++
			if e.Token != "tok-keep" {
				t.Errorf("kept token = %q", e.Token)
			}
		}
	}
	if kept != 1 {
		t.Errorf("exactly one entry should be marked keep, got %d", kept)
	}
}

// Without confirm=true nothing may be deleted, however many duplicates exist.
func TestDedupeRequiresConfirmation(t *testing.T) {
	srv := newDedupeServer()
	srv.add("root-token", "a.txt", "tok-keep", 100)
	srv.add("root-token", "a.txt", "tok-extra", 200)

	f, _ := dedupeFs(t, srv, map[string]string{"a.txt": "tok-keep"})

	rep, err := f.dedupeCommand(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("dedupe: %v", err)
	}
	if len(rep.Deleted) != 0 {
		t.Errorf("deleted %v without confirmation", rep.Deleted)
	}
	if !rep.DryRun {
		t.Error("report should be marked as a dry run")
	}
	if !strings.Contains(rep.Summary, "confirm=true") {
		t.Errorf("summary should tell the user how to proceed\ngot: %q", rep.Summary)
	}
	if len(srv.deleted) != 0 {
		t.Errorf("the server saw deletions: %v", srv.deleted)
	}
}

// With no snapshot there is no way to tell our copy from someone else's, so the
// command must refuse rather than pick one.
func TestDedupeRefusesWithoutState(t *testing.T) {
	srv := newDedupeServer()
	srv.add("root-token", "a.txt", "tok-1", 100)
	srv.add("root-token", "a.txt", "tok-2", 200)

	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {})
	c.listFolderFn = srv.handler(t)
	f := &Fs{name: "test", client: c}
	f.resolver = newDirResolver(c, "root-token", "", "")

	rep, err := f.dedupeCommand(context.Background(), "", map[string]string{"confirm": "true"})
	if err != nil {
		t.Fatalf("dedupe: %v", err)
	}
	if len(rep.Deleted) != 0 {
		t.Errorf("deleted %v with no state to justify it", rep.Deleted)
	}
	if !strings.Contains(rep.Summary, "no state_file") {
		t.Errorf("summary should name the missing prerequisite\ngot: %q", rep.Summary)
	}
}

// If the recorded token is not in the folder, the snapshot no longer describes
// reality; deleting "the others" could destroy a fresh upload.
func TestDedupeRefusesWhenRecordedTokenMissing(t *testing.T) {
	srv := newDedupeServer()
	srv.add("root-token", "a.txt", "tok-x", 100)
	srv.add("root-token", "a.txt", "tok-y", 200)

	f, _ := dedupeFs(t, srv, map[string]string{"a.txt": "tok-gone"})

	rep, err := f.dedupeCommand(context.Background(), "", map[string]string{"confirm": "true"})
	if err != nil {
		t.Fatalf("dedupe: %v", err)
	}
	if len(rep.Deleted) != 0 {
		t.Errorf("deleted %v although the recorded token is absent", rep.Deleted)
	}
	if len(rep.Skipped) != 1 {
		t.Fatalf("expected one skipped group, got %v", rep.Skipped)
	}
	if !strings.Contains(rep.Skipped[0], "tok-gone") {
		t.Errorf("skip reason should quote the recorded token\ngot: %q", rep.Skipped[0])
	}
}

// Repair mode means the snapshot was just rebuilt and proves nothing.
func TestDedupeRefusesInRepairMode(t *testing.T) {
	srv := newDedupeServer()
	srv.add("root-token", "a.txt", "tok-keep", 100)
	srv.add("root-token", "a.txt", "tok-extra", 200)

	f, _ := dedupeFs(t, srv, map[string]string{"a.txt": "tok-keep"})
	f.repair = true

	rep, err := f.dedupeCommand(context.Background(), "", map[string]string{"confirm": "true"})
	if err != nil {
		t.Fatalf("dedupe: %v", err)
	}
	if len(rep.Deleted) != 0 {
		t.Errorf("deleted %v in repair mode", rep.Deleted)
	}
}

// Names with a single entry are not duplicates and must be ignored.
func TestFindDuplicatesIgnoresUniqueNames(t *testing.T) {
	srv := newDedupeServer()
	srv.add("root-token", "only.txt", "tok-1", 100)
	srv.add("root-token", "other.txt", "tok-2", 100)

	f, _ := dedupeFs(t, srv, nil)
	groups, err := f.findDuplicates(context.Background(), "")
	if err != nil {
		t.Fatalf("findDuplicates: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("found %d groups, want 0: %+v", len(groups), groups)
	}
}

var _ = fmt.Sprintf
