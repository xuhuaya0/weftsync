package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Concurrent uploads into a NEW folder must create it exactly once.
//
// rclone runs up to 8 transfers in parallel. Every one of them asks "does the
// target folder exist?", gets "no" for a new tree, and creates its own copy. On
// the real tenant that produced four folders all named like the target, each
// holding a couple of the files - measured while looking into F9 stage two:
//
//	4x folder  s2raw  token=[Uytv…, LVcu…, XIZd…, Y7sm…]
//
// The damage is not cosmetic: a listing resolves the name to ONE of the copies
// and reports the files held by the others as `remote-deleted`. With
// propagate_remote_delete on, that deletes local files that are perfectly fine.
func TestConcurrentUploadCreatesOneFolder(t *testing.T) {
	srv := &folderRaceServer{folders: map[string]string{"root-token": ""}}
	f := raceTestFs(t, srv)

	const files = 8
	var wg sync.WaitGroup
	errs := make([]error, files)
	wg.Add(files)
	for i := 0; i < files; i++ {
		go func(i int) {
			defer wg.Done()
			src := testSrc{
				remote: fmt.Sprintf("newdir/f%d.txt", i),
				size:   3,
			}
			_, errs[i] = f.Put(context.Background(), strings.NewReader("abc"), src)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}

	created := srv.foldersNamed("newdir")
	if len(created) != 1 {
		t.Fatalf("%d folders named %q were created (%v); concurrent uploads must "+
			"agree on one, or files end up split across copies nobody can see",
			len(created), "newdir", created)
	}
	// Every file must be in that one folder, not scattered.
	if got := srv.childrenOf(created[0]); got != files {
		t.Errorf("the single folder holds %d file(s), want %d", got, files)
	}
}

// A second, sequential upload into the same folder must not create another copy.
func TestRepeatedUploadReusesTheFolder(t *testing.T) {
	srv := &folderRaceServer{folders: map[string]string{"root-token": ""}}
	f := raceTestFs(t, srv)

	for i := 0; i < 3; i++ {
		src := testSrc{remote: fmt.Sprintf("dir/f%d.txt", i), size: 3}
		if _, err := f.Put(context.Background(), strings.NewReader("abc"), src); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	if n := len(srv.foldersNamed("dir")); n != 1 {
		t.Errorf("%d folders named %q, want 1", n, "dir")
	}
}

// folderRaceServer models folder creation and listing, counting duplicates.
type folderRaceServer struct {
	mu   sync.Mutex
	next int
	// folders maps a folder token to its name; "root-token" is the Fs root.
	folders map[string]string
	// entries maps a folder token to its children (name -> isDir).
	children map[string]map[string]bool
	// tokens maps "parent/name" to the token handed out for it.
	byName map[string][]string
}

func (s *folderRaceServer) init() {
	if s.children == nil {
		s.children = map[string]map[string]bool{"root-token": {}}
	}
	if s.byName == nil {
		s.byName = map[string][]string{}
	}
}

func (s *folderRaceServer) foldersNamed(name string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.byName["root-token/"+name]...)
}

func (s *folderRaceServer) childrenOf(token string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, isDir := range s.children[token] {
		if !isDir {
			n++
		}
	}
	return n
}

func (s *folderRaceServer) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	s.init()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/files"):
			token := r.URL.Query().Get("folder_token")
			s.mu.Lock()
			files := []map[string]any{}
			for name, isDir := range s.children[token] {
				typ := "file"
				if isDir {
					typ = "folder"
				}
				// A folder's token is looked up by name under this parent.
				childToken := ""
				for _, tok := range s.byName[token+"/"+name] {
					childToken = tok
					break
				}
				if !isDir {
					childToken = "file-" + name
				}
				files = append(files, map[string]any{
					"name": name, "token": childToken, "type": typ,
					"parent_token": token,
					"created_time": "1700000000", "modified_time": "1700000000",
				})
			}
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "data": map[string]any{"files": files, "has_more": false},
			})

		case strings.HasSuffix(r.URL.Path, "/files/create_folder"):
			var req struct {
				Name        string `json:"name"`
				FolderToken string `json:"folder_token"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)

			s.mu.Lock()
			s.next++
			token := "folder-" + strconv.Itoa(s.next)
			s.folders[token] = req.Name
			s.children[token] = map[string]bool{}
			s.children[req.FolderToken][req.Name] = true
			s.byName[req.FolderToken+"/"+req.Name] = append(
				s.byName[req.FolderToken+"/"+req.Name], token)
			s.mu.Unlock()

			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "data": map[string]any{"token": token},
			})

		case strings.HasSuffix(r.URL.Path, "/files/upload_all"):
			if err := r.ParseMultipartForm(8 << 20); err != nil {
				t.Errorf("parse multipart: %v", err)
				return
			}
			parent := r.FormValue("parent_node")
			name := r.FormValue("file_name")
			if _, _, err := r.FormFile("file"); err != nil {
				t.Errorf("missing file part: %v", err)
				return
			}
			s.mu.Lock()
			if s.children[parent] == nil {
				s.children[parent] = map[string]bool{}
			}
			s.children[parent][name] = false
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "data": map[string]any{"file_token": "file-" + name},
			})

		default:
			// Anything else (e.g. the block protocol) is not exercised here.
			_, _ = io.Copy(io.Discard, r.Body)
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}
}

func raceTestFs(t *testing.T, srv *folderRaceServer) *Fs {
	t.Helper()
	c, _ := testClient(t, srv.handler(t))
	f := &Fs{name: "test", client: c, storable: true}
	f.resolver = newDirResolver(c, "root-token", "", "")
	return f
}
