package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/hash"
	"weftsync/state"
)

// testSrc is a minimal fs.ObjectInfo for exercise Put/Update.
type testSrc struct {
	remote  string
	size    int64
	modTime time.Time
}

func (s testSrc) Fs() fs.Info                                     { return nil }
func (s testSrc) String() string                                  { return s.remote }
func (s testSrc) Remote() string                                  { return s.remote }
func (s testSrc) Size() int64                                     { return s.size }
func (s testSrc) ModTime(context.Context) time.Time               { return s.modTime }
func (s testSrc) Hash(context.Context, hash.Type) (string, error) { return "", nil }
func (s testSrc) Storable() bool                                  { return true }

// replaceServer models one folder that may hold SEVERAL same-named files, which
// is what Feishu actually does: uploading never replaces, it adds a new entry
// with a new token (E1/E2), so a replacement exists as two entries until the old
// one is deleted.
type replaceServer struct {
	mu      sync.Mutex
	name    string
	tokens  []string // every live entry with that name, oldest first
	deleted []string
	uploads int
}

func (s *replaceServer) handle(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		s.mu.Lock()
		defer s.mu.Unlock()

		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/files"):
			files := []map[string]any{}
			for _, tok := range s.tokens {
				files = append(files, map[string]any{
					"name":          s.name,
					"token":         tok,
					"type":          "file",
					"parent_token":  "root-token",
					"modified_time": "1700000000",
					"created_time":  "1700000000",
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"files": files, "has_more": false},
			})

		case strings.HasSuffix(r.URL.Path, "/files/upload_all"):
			s.uploads++
			s.tokens = append(s.tokens, "new-token")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"file_token": "new-token"},
			})

		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/files/"):
			tok := filepath.Base(r.URL.Path)
			s.deleted = append(s.deleted, tok)
			for i, have := range s.tokens {
				if have == tok {
					s.tokens = append(s.tokens[:i], s.tokens[i+1:]...)
					break
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{}})

		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}
}

// newReplaceFs builds an Fs backed by a snapshot that remembers oldToken for
// name, which is the situation a second sync run is in.
func newReplaceFs(t *testing.T, srv *replaceServer, name, oldToken string) (*Fs, string) {
	t.Helper()

	c, _ := testClient(t, srv.handle(t))

	const pairID = "0123456789abcdef"
	statePath := filepath.Join(t.TempDir(), pairID+".state.json")

	snap := state.New(pairID)
	snap.SetPath(statePath)
	snap.Put(name, state.Entry{
		LocalMTime:  1700000000,
		LocalSize:   3,
		RemoteToken: oldToken,
		SyncedAt:    1700000000,
	})
	if err := state.Save(snap); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	loaded, err := state.Load(statePath)
	if err != nil || loaded == nil {
		t.Fatalf("reload state: %v (nil=%v)", err, loaded == nil)
	}

	f := &Fs{
		name:     "test",
		client:   c,
		st:       loaded,
		stPath:   statePath,
		storable: true,
	}
	f.resolver = newDirResolver(c, "root-token", "", "")
	return f, statePath
}

// Regression for a real defect found on the live tenant: Update() looked up the
// previous token AFTER calling Put(), which had already rewritten the snapshot
// with the new token. oldToken therefore equalled newToken and the deletion was
// skipped every time, leaving a duplicate copy of every replaced file.
//
// It stayed hidden because List() collapses duplicate names, so `lsf` reported a
// healthy file count. Only a raw listing (or this test) can see it.
func TestUpdateDeletesPreviousToken(t *testing.T) {
	srv := &replaceServer{name: "f.bin", tokens: []string{"old-token"}}
	f, statePath := newReplaceFs(t, srv, "f.bin", "old-token")
	ctx := context.Background()

	obj, err := f.NewObject(ctx, "f.bin")
	if err != nil {
		t.Fatalf("NewObject: %v", err)
	}

	body := "abcdefgh"
	src := testSrc{remote: "f.bin", size: int64(len(body)), modTime: time.Unix(1700009999, 0)}
	if err := obj.Update(ctx, strings.NewReader(body), src); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if len(srv.deleted) != 1 || srv.deleted[0] != "old-token" {
		t.Fatalf("deleted tokens = %v, want exactly [old-token]; "+
			"the previous version is still in the folder", srv.deleted)
	}

	// The snapshot must now describe the new version, so the next run skips it.
	reloaded, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	e, ok := reloaded.Lookup("f.bin")
	if !ok {
		t.Fatal("state lost the entry")
	}
	if e.RemoteToken != "new-token" {
		t.Errorf("state token = %q, want new-token", e.RemoteToken)
	}
}

// Repair mode must not delete anything, even though the upload succeeds.
func TestUpdateKeepsOldTokenInRepairMode(t *testing.T) {
	srv := &replaceServer{name: "f.bin", tokens: []string{"old-token"}}
	f, _ := newReplaceFs(t, srv, "f.bin", "old-token")
	f.repair = true
	f.repairReason = "test"

	ctx := context.Background()
	obj, err := f.NewObject(ctx, "f.bin")
	if err != nil {
		t.Fatalf("NewObject: %v", err)
	}

	body := "abcdefgh"
	src := testSrc{remote: "f.bin", size: int64(len(body)), modTime: time.Unix(1700009999, 0)}
	if err := obj.Update(ctx, strings.NewReader(body), src); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if len(srv.deleted) != 0 {
		t.Errorf("repair mode deleted %v; it must delete nothing", srv.deleted)
	}
}

// The snapshot records what Put uploaded so the next run can skip the file.
func TestPutRecordsSnapshot(t *testing.T) {
	srv := &replaceServer{name: "f.bin"}
	f, statePath := newReplaceFs(t, srv, "f.bin", "old-token")
	// Pretend it is a first run: no prior knowledge.
	f.st = state.New("0123456789abcdef")
	f.st.SetPath(statePath)

	ctx := context.Background()
	obj, err := f.Put(ctx, strings.NewReader("abc"), testSrc{
		remote: "f.bin", size: 3, modTime: time.Unix(1700001234, 0),
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if obj.Remote() != "f.bin" {
		t.Errorf("remote = %q", obj.Remote())
	}

	reloaded, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	e, ok := reloaded.Lookup("f.bin")
	if !ok {
		t.Fatal("Put did not record the upload")
	}
	if e.RemoteToken != "new-token" || e.LocalSize != 3 || e.LocalMTime != 1700001234 {
		t.Errorf("entry = %+v", e)
	}
}

// Keep the unused-import checker honest about os in this file's future edits.
var _ = os.Getpid
