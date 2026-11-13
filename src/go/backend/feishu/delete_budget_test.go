package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"weftsync/state"
)

// A real run failed twice in a row replacing two files: the upload succeeded, the
// delete of the previous version returned code 1061001 (server internal) until the
// 3-attempt budget ran out, and the folder kept two same-named entries that List
// hides. These tests cover the three parts of the fix - a larger budget for
// deletes, "already gone" counted as success, and an error that says what is
// actually true.

// deleteServer models a folder that holds one file and answers deletes from a
// script: each delete pops the next response.
type deleteServer struct {
	mu        sync.Mutex
	name      string
	tokens    []string
	deletes   int
	responses []map[string]any // consumed one per delete; the last one repeats
	uploaded  int
}

func (s *deleteServer) handle(t *testing.T) http.HandlerFunc {
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
			s.uploaded++
			s.tokens = append(s.tokens, "new-token")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"file_token": "new-token"},
			})

		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/files/"):
			resp := map[string]any{"code": 0, "data": map[string]any{}}
			if len(s.responses) > 0 {
				resp = s.responses[0]
				if len(s.responses) > 1 {
					s.responses = s.responses[1:]
				}
			}
			// A successful delete is the only one that changes the folder.
			if code, _ := resp["code"].(int); code == 0 {
				tok := filepath.Base(r.URL.Path)
				for i, have := range s.tokens {
					if have == tok {
						s.tokens = append(s.tokens[:i], s.tokens[i+1:]...)
						break
					}
				}
			}
			s.deletes++
			_ = json.NewEncoder(w).Encode(resp)

		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}
}

// newDeleteFs builds an Fs whose snapshot remembers oldToken for name, which is
// the state a second sync run starts from.
func newDeleteFs(t *testing.T, srv *deleteServer, name, oldToken string) *Fs {
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

	f := &Fs{
		name:     "test",
		client:   c,
		st:       snap,
		stPath:   statePath,
		storable: true,
	}
	f.resolver = newDirResolver(c, "root-token", "", "")
	return f
}

// replaceWith runs Update on an existing object, the way a second sync would.
func replaceWith(t *testing.T, f *Fs, name string) error {
	t.Helper()

	ctx := context.Background()
	obj, err := f.NewObject(ctx, name)
	if err != nil {
		t.Fatalf("NewObject: %v", err)
	}
	body := "abcdefgh"
	return obj.Update(ctx, strings.NewReader(body), testSrc{
		remote: name, size: int64(len(body)), modTime: time.Unix(1700009999, 0),
	})
}

// A delete gets a larger retry budget than every other call, because its failure
// is the expensive one: the new version is already uploaded, so a leftover token
// becomes an invisible duplicate.
func TestDeleteGetsMoreAttemptsThanOtherCalls(t *testing.T) {
	if deleteAttempts <= maxAttempts {
		t.Fatalf("deleteAttempts = %d, must exceed maxAttempts = %d; "+
			"otherwise a transient files.delete failure still strands a duplicate",
			deleteAttempts, maxAttempts)
	}

	srv := &deleteServer{
		name:      "f.bin",
		tokens:    []string{"old-token"},
		responses: []map[string]any{{"code": CodeInternal, "msg": "unknown error"}},
	}
	f := newDeleteFs(t, srv, "f.bin", "old-token")

	err := replaceWith(t, f, "f.bin")
	if err == nil {
		t.Fatal("expected the delete to fail after exhausting its budget")
	}
	if srv.deletes != deleteAttempts {
		t.Errorf("files.delete attempts = %d, want %d", srv.deletes, deleteAttempts)
	}
	if srv.uploaded != 1 {
		t.Errorf("uploads = %d, want 1; the budget must not re-upload", srv.uploaded)
	}
}

// The endpoint says the token is already gone: that is what the caller wanted, so
// the replacement is a success. Not tolerating this turns a lost response into a
// permanent failure.
func TestDeleteTreatsGoneAsSuccess(t *testing.T) {
	for _, code := range []int{CodeFileDeleted, CodeNotFound, CodeParentNotFound} {
		srv := &deleteServer{
			name:      "f.bin",
			tokens:    []string{"old-token"},
			responses: []map[string]any{{"code": code, "msg": "node not found"}},
		}
		f := newDeleteFs(t, srv, "f.bin", "old-token")

		if err := replaceWith(t, f, "f.bin"); err != nil {
			t.Errorf("code %d from files.delete made Update fail: %v; "+
				"a token that is already gone is the desired end state", code, err)
		}
		if srv.deletes != 1 {
			t.Errorf("code %d: attempts = %d, want 1 (no retry for a gone token)",
				code, srv.deletes)
		}
	}
}

// When the delete does exhaust its budget, the error must not read like a failed
// transfer. The content is uploaded and recorded; what remains is a duplicate, and
// the message has to name it and the command that removes it.
func TestDuplicateLeftErrorSaysWhatIsTrue(t *testing.T) {
	srv := &deleteServer{
		name:      "R5.xlsx",
		tokens:    []string{"old-token"},
		responses: []map[string]any{{"code": CodeInternal, "msg": "unknown error"}},
	}
	f := newDeleteFs(t, srv, "R5.xlsx", "old-token")

	err := replaceWith(t, f, "R5.xlsx")
	if err == nil {
		t.Fatal("expected an error")
	}

	var dupErr *DuplicateLeftError
	if !errors.As(err, &dupErr) {
		t.Fatalf("error type = %T, want *DuplicateLeftError\ngot: %v", err, err)
	}
	if dupErr.NewToken != "new-token" || dupErr.OldToken != "old-token" {
		t.Errorf("tokens = new %q old %q", dupErr.NewToken, dupErr.OldToken)
	}

	// The underlying API error stays inspectable, so a caller can still tell a
	// transient failure from a permanent one.
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Errorf("the Feishu error is no longer reachable through errors.As")
	}

	msg := err.Error()
	for _, want := range []string{
		"new-token",      // what is now in the cloud
		"old-token",      // which entry is stranded
		"R5.xlsx",        // which file
		"test:",          // a runnable remote:dir argument
		"backend dedupe", // the command that fixes it
		"IS uploaded",    // state the outcome, not just the failure
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q\ngot: %s", want, msg)
		}
	}
	// It must not read like a failed upload: the content is in the cloud.
	for _, unwanted := range []string{"upload failed", "could not upload", "replace failed"} {
		if strings.Contains(msg, unwanted) {
			t.Errorf("message reads as a failed transfer (%q)\ngot: %s", unwanted, msg)
		}
	}
}

// The cleanup command has to be runnable as printed, including for a file at the
// remote root where there is no folder to name.
func TestDuplicateLeftErrorCommandTarget(t *testing.T) {
	cases := []struct{ remote, want string }{
		{"R5.xlsx", "test:"},
		{"ADC_CapCal/R5.xlsx", "test:ADC_CapCal"},
		{"a/b/c.xlsx", "test:a/b"},
	}
	for _, c := range cases {
		e := &DuplicateLeftError{Remote: c.remote, RemoteName: "test"}
		if got := e.target(); got != c.want {
			t.Errorf("target(%q) = %q, want %q", c.remote, got, c.want)
		}
	}
}
