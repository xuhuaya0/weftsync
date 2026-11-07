package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"weftsync/state"
)

// treeServer is a small in-memory model of a Feishu drive: folders identified by
// token, nodes carrying a name and a parent, and a move endpoint that relocates
// a node WITHOUT changing its token (which is what the live tenant does).
type treeServer struct {
	mu      sync.Mutex
	nodes   []*treeNode
	nextID  int
	moves   []string
	deletes []string
	uploads int
}

type treeNode struct {
	Token   string
	Name    string
	Parent  string
	IsDir   bool
	Content []byte
}

func newTreeServer() *treeServer {
	s := &treeServer{}
	s.addDir("root-token", "")
	return s
}

func (s *treeServer) addDir(token, name string) *treeNode {
	s.nextID++
	s.nodes = append(s.nodes, &treeNode{
		Token: token, Name: name, Parent: "root-token", IsDir: true,
	})
	return s.nodes[len(s.nodes)-1]
}

// findDir resolves a folder by name under a parent.
func (s *treeServer) findDir(parent, name string) string {
	for _, n := range s.nodes {
		if n.IsDir && n.Parent == parent && n.Name == name {
			return n.Token
		}
	}
	return ""
}

func (s *treeServer) childrenOf(parent string) []*treeNode {
	var out []*treeNode
	for _, n := range s.nodes {
		if n.Parent == parent {
			out = append(out, n)
		}
	}
	return out
}

func (s *treeServer) byToken(token string) *treeNode {
	for _, n := range s.nodes {
		if n.Token == token {
			return n
		}
	}
	return nil
}

func (s *treeServer) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		s.mu.Lock()
		defer s.mu.Unlock()

		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/files"):
			parent := r.URL.Query().Get("folder_token")
			files := []map[string]any{}
			for _, n := range s.childrenOf(parent) {
				typ := "file"
				if n.IsDir {
					typ = "folder"
				}
				files = append(files, map[string]any{
					"name":          n.Name,
					"token":         n.Token,
					"type":          typ,
					"parent_token":  n.Parent,
					"modified_time": "1700000000",
					"created_time":  "1700000000",
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"files": files, "has_more": false},
			})

		case strings.HasSuffix(path, "/files/create_folder"):
			var req struct {
				Name        string `json:"name"`
				FolderToken string `json:"folder_token"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			s.nextID++
			token := fmt.Sprintf("dir-%d", s.nextID)
			s.nodes = append(s.nodes, &treeNode{
				Token: token, Name: req.Name, Parent: req.FolderToken, IsDir: true,
			})
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "data": map[string]any{"token": token},
			})

		case strings.HasSuffix(path, "/files/upload_all"):
			s.uploads++
			s.nextID++
			name := r.FormValue("file_name")
			parent := r.FormValue("parent_node")
			token := fmt.Sprintf("uploaded-%d", s.nextID)
			s.nodes = append(s.nodes, &treeNode{
				Token: token, Name: name, Parent: parent, Content: []byte("x"),
			})
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "data": map[string]any{"file_token": token},
			})

		case strings.HasSuffix(path, "/move"):
			var req struct {
				Type        string `json:"type"`
				FolderToken string `json:"folder_token"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			token := filepath.Base(strings.TrimSuffix(path, "/move"))
			n := s.byToken(token)
			if n == nil {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": CodeNotFound, "msg": "not found",
				})
				return
			}
			if s.byToken(req.FolderToken) == nil {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": CodeDestinationNoPerm, "msg": "destination parent no permission",
				})
				return
			}
			n.Parent = req.FolderToken
			s.moves = append(s.moves, token)
			// A file move answers {}; the token is untouched.
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{}})

		case r.Method == http.MethodDelete && strings.Contains(path, "/files/"):
			token := filepath.Base(path)
			s.deletes = append(s.deletes, token)
			for i, n := range s.nodes {
				if n.Token == token {
					s.nodes = append(s.nodes[:i], s.nodes[i+1:]...)
					break
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{}})

		case strings.HasSuffix(path, "/download"):
			token := strings.TrimSuffix(strings.TrimPrefix(path, "/open-apis/drive/v1/files/"), "/download")
			n := s.byToken(token)
			if n == nil {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": CodeNotFound, "msg": "gone"})
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(n.Content)

		default:
			t.Errorf("unexpected request %s %s", r.Method, path)
		}
	}
}

func (s *treeServer) addFile(parent, name, token string, content []byte) {
	s.nodes = append(s.nodes, &treeNode{
		Token: token, Name: name, Parent: parent, Content: content,
	})
}

// moveTestFs builds an Fs over the fake tree, with a snapshot that already knows
// about the file at its original path.
func moveTestFs(t *testing.T, srv *treeServer, seed []state.Entry) (*Fs, string) {
	t.Helper()

	c, _ := testClient(t, srv.handler(t))
	const pairID = "abcdefabcdefabcd"
	statePath := filepath.Join(t.TempDir(), pairID+".state.json")

	snap := state.New(pairID)
	snap.SetPath(statePath)
	for _, e := range seed {
		snap.Put(e.Path, e)
	}
	if err := state.Save(snap); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	loaded, err := state.Load(statePath)
	if err != nil || loaded == nil {
		t.Fatalf("reload state: %v", err)
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

// Relocating a file within the remote must use the server-side move and leave
// the token untouched, so the snapshot stays valid.
// Regression for a real defect found on the live tenant: rclone builds the
// destination Fs with the destination's own root (moveto "a/x" "a/sub/x" gives a
// Fs rooted at "sub"), so `remote` and src.Remote() live in different path
// frames. Comparing them directly made every such move look like a no-op: the
// file never moved, yet the command reported "Moved (server-side)".
func TestMoveAcrossDifferentFsRoots(t *testing.T) {
	srv := newTreeServer()
	srv.addDir("dir-a", "a")
	srv.addFile("root-token", "doc.txt", "tok-1", []byte("hello"))

	f, _ := moveTestFs(t, srv, nil)
	ctx := context.Background()

	obj, err := f.NewObject(ctx, "doc.txt")
	if err != nil {
		t.Fatalf("NewObject: %v", err)
	}

	// A second Fs over the same remote, but rooted at "a" -this is what rclone
	// hands to Move for a moveto into that folder.
	destFs := &Fs{
		name: "test", root: "a", client: f.client,
		st: f.st, stPath: f.stPath, storable: true,
	}
	destFs.resolver = newDirResolver(f.client, "root-token", "a", "a")

	moved, err := destFs.Move(ctx, obj, "doc.txt")
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if moved.Remote() != "doc.txt" {
		t.Errorf("remote = %q", moved.Remote())
	}

	// The whole point: the node must actually be inside folder "a" now.
	if got := srv.byToken("tok-1"); got == nil || got.Parent != "dir-a" {
		t.Fatalf("node did not move: %+v (root folder still holds %d child(ren))",
			got, len(srv.childrenOf("root-token")))
	}
	if n := len(srv.childrenOf("dir-a")); n != 1 {
		t.Errorf("destination folder holds %d entries, want 1", n)
	}
	if len(srv.moves) != 1 {
		t.Errorf("moves = %v, want exactly one move call", srv.moves)
	}
}

// Moving into the very place it already is must stay a no-op, in any frame.
func TestMoveAbsoluteSamePathIsNoOp(t *testing.T) {
	srv := newTreeServer()
	srv.addFile("root-token", "doc.txt", "tok-1", []byte("x"))

	f, _ := moveTestFs(t, srv, nil)
	ctx := context.Background()

	obj, err := f.NewObject(ctx, "doc.txt")
	if err != nil {
		t.Fatalf("NewObject: %v", err)
	}
	if _, err := f.Move(ctx, obj, "doc.txt"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if len(srv.moves) != 0 || srv.uploads != 0 {
		t.Errorf("no work should happen: moves=%v uploads=%d", srv.moves, srv.uploads)
	}
}

func TestMoveRelocatesWithoutChangingToken(t *testing.T) {
	srv := newTreeServer()
	srv.addDir("dir-a", "a")
	srv.addFile("root-token", "doc.txt", "tok-1", []byte("hello"))

	f, statePath := moveTestFs(t, srv, []state.Entry{{
		Path: "doc.txt", LocalMTime: 1700000000, LocalSize: 5,
		RemoteToken: "tok-1", SyncedAt: 1700000000,
	}})
	ctx := context.Background()

	obj, err := f.NewObject(ctx, "doc.txt")
	if err != nil {
		t.Fatalf("NewObject: %v", err)
	}

	moved, err := f.Move(ctx, obj, "a/doc.txt")
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if moved.Remote() != "a/doc.txt" {
		t.Errorf("remote = %q, want a/doc.txt", moved.Remote())
	}
	if got := srv.byToken("tok-1"); got == nil || got.Parent != "dir-a" {
		t.Errorf("server did not relocate the node: %+v", got)
	}
	if len(srv.moves) != 1 || srv.moves[0] != "tok-1" {
		t.Errorf("moves = %v, want exactly [tok-1]", srv.moves)
	}
	if srv.uploads != 0 {
		t.Errorf("a pure relocation must not re-upload (%d uploads)", srv.uploads)
	}

	reloaded, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := reloaded.Lookup("doc.txt"); ok {
		t.Error("old path should be gone from the snapshot")
	}
	e, ok := reloaded.Lookup("a/doc.txt")
	if !ok {
		t.Fatal("moved file should be recorded at its new path")
	}
	if e.RemoteToken != "tok-1" {
		t.Errorf("token = %q, want tok-1 (unchanged)", e.RemoteToken)
	}
}

// A rename cannot be done server side, so it becomes move + upload + delete.
// The upload must happen before the delete so a failure leaves the data reachable.
func TestMoveRenamesByCopyingThenDeleting(t *testing.T) {
	srv := newTreeServer()
	srv.addDir("dir-a", "a")
	srv.addFile("root-token", "old.txt", "tok-1", []byte("payload"))

	f, statePath := moveTestFs(t, srv, []state.Entry{{
		Path: "old.txt", LocalMTime: 1700000000, LocalSize: 7,
		RemoteToken: "tok-1", SyncedAt: 1700000000,
	}})
	ctx := context.Background()

	obj, err := f.NewObject(ctx, "old.txt")
	if err != nil {
		t.Fatalf("NewObject: %v", err)
	}

	moved, err := f.Move(ctx, obj, "a/new.txt")
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if moved.Remote() != "a/new.txt" {
		t.Errorf("remote = %q", moved.Remote())
	}
	if srv.uploads != 1 {
		t.Errorf("uploads = %d, want 1 (rename needs a new upload)", srv.uploads)
	}
	if len(srv.deletes) != 1 || srv.deletes[0] != "tok-1" {
		t.Errorf("deletes = %v, want exactly [tok-1]", srv.deletes)
	}

	var names []string
	for _, n := range srv.childrenOf("dir-a") {
		names = append(names, n.Name)
	}
	if len(names) != 1 || names[0] != "new.txt" {
		t.Errorf("destination folder = %v, want [new.txt]", names)
	}

	reloaded, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := reloaded.Lookup("old.txt"); ok {
		t.Error("old path should be gone")
	}
	if _, ok := reloaded.Lookup("a/new.txt"); !ok {
		t.Error("new path should be recorded")
	}
}

// In repair mode nothing may be deleted, so a rename must leave the original.
func TestMoveRenameKeepsOriginalInRepairMode(t *testing.T) {
	srv := newTreeServer()
	srv.addDir("dir-a", "a")
	srv.addFile("root-token", "old.txt", "tok-1", []byte("payload"))

	f, _ := moveTestFs(t, srv, []state.Entry{{
		Path: "old.txt", LocalMTime: 1700000000, LocalSize: 7,
		RemoteToken: "tok-1", SyncedAt: 1700000000,
	}})
	f.repair = true
	ctx := context.Background()

	obj, err := f.NewObject(ctx, "old.txt")
	if err != nil {
		t.Fatalf("NewObject: %v", err)
	}
	if _, err := f.Move(ctx, obj, "a/new.txt"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if len(srv.deletes) != 0 {
		t.Errorf("repair mode deleted %v; it must delete nothing", srv.deletes)
	}
}

// A destination folder that does not exist must be created, mirroring Put.
func TestMoveCreatesDestinationFolder(t *testing.T) {
	srv := newTreeServer()
	srv.addFile("root-token", "doc.txt", "tok-1", []byte("x"))

	f, _ := moveTestFs(t, srv, nil)
	ctx := context.Background()

	obj, err := f.NewObject(ctx, "doc.txt")
	if err != nil {
		t.Fatalf("NewObject: %v", err)
	}
	if _, err := f.Move(ctx, obj, "fresh/deep/doc.txt"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	deep := srv.findDir("root-token", "fresh")
	if deep == "" {
		t.Fatal("destination tree was not created")
	}
}

var _ = time.Now
