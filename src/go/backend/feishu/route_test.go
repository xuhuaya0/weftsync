package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"weftsync/state"
)

// The 20MB split point decides which upload protocol a file takes, and getting it
// wrong is silent: a file judged "small" that is actually larger would be cut
// short by the io.LimitReader in Put and uploaded truncated, with no error and a
// token that looks perfectly healthy.
//
// Feishu rejects upload_all above 20MB with code 1061002 (measured during M0), so
// the boundary is the server's, not ours.
func TestUploadRoutePicksTheRightProtocol(t *testing.T) {
	if MaxDirectUpload != 20*1024*1024 {
		t.Fatalf("MaxDirectUpload = %d, want 20 MiB; the constant encodes a measured "+
			"server limit, not a preference", MaxDirectUpload)
	}

	const (
		blockSize = 4 * 1024 * 1024
		over      = MaxDirectUpload + 1
	)

	cases := []struct {
		name string
		// declared is what fs.ObjectInfo reports; actual is how many bytes the
		// stream really produces. They differ only in the "lying source" cases.
		declared int64
		actual   int64
		want     string // "direct", "chunked", or "error"
	}{
		{"tiny", 3, 3, "direct"},
		{"exactly at the limit", MaxDirectUpload, MaxDirectUpload, "direct"},
		{"one byte over the limit", over, over, "chunked"},
		{"unknown size, small", -1, 3, "direct"},
		{"unknown size, over the limit", -1, over, "error"},
		{"declared small but actually over", 3, over, "error"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newRouteServer(blockSize)
			f := routeTestFs(t, srv)

			src := testSrc{remote: "f.bin", size: c.declared,
				modTime: time.Unix(1700000000, 0)}

			_, err := f.Put(context.Background(), io.LimitReader(zeroReader{}, c.actual), src)

			switch c.want {
			case "direct":
				if err != nil {
					t.Fatalf("Put: %v", err)
				}
				if srv.direct != 1 || srv.prepare != 0 {
					t.Errorf("direct=%d prepare=%d, want the single-request path",
						srv.direct, srv.prepare)
				}
				// The whole stream must have reached the server, not a prefix.
				if srv.directBytes != c.actual {
					t.Errorf("upload_all received %d bytes, want %d", srv.directBytes, c.actual)
				}

			case "chunked":
				if err != nil {
					t.Fatalf("Put: %v", err)
				}
				if srv.prepare != 1 || srv.direct != 0 {
					t.Errorf("prepare=%d direct=%d, want the block path", srv.prepare, srv.direct)
				}
				if srv.chunkedBytes != c.actual {
					t.Errorf("blocks carried %d bytes, want %d", srv.chunkedBytes, c.actual)
				}
				if !srv.finished {
					t.Error("upload_finish was never called")
				}

			case "error":
				if err == nil {
					t.Fatal("a source that cannot be sized must be refused, not uploaded")
				}
				tooBig, ok := err.(*ErrFileTooLarge)
				if !ok {
					t.Fatalf("error = %T %v, want *ErrFileTooLarge", err, err)
				}
				if !tooBig.SizeUnknown {
					t.Error("SizeUnknown must be set: the declared size was wrong, " +
						"which needs a different explanation from 'too big'")
				}
				// Nothing may have been committed.
				if srv.direct != 0 || srv.finished {
					t.Errorf("direct=%d finished=%v, want nothing committed",
						srv.direct, srv.finished)
				}
			}
		})
	}
}

// A declared size of 0 for a non-empty stream is the same untrustworthy case, and
// it is the one a caller is most likely to produce by accident (a pipe, a
// decompressor mid-stream).
func TestUploadRejectsAnEmptyDeclarationWithContent(t *testing.T) {
	srv := newRouteServer(4 * 1024 * 1024)
	f := routeTestFs(t, srv)

	src := testSrc{remote: "f.bin", size: 0, modTime: time.Unix(1700000000, 0)}
	_, err := f.Put(context.Background(), io.LimitReader(zeroReader{}, MaxDirectUpload+1), src)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if _, ok := err.(*ErrFileTooLarge); !ok {
		t.Fatalf("error = %T %v, want *ErrFileTooLarge", err, err)
	}
	if srv.direct != 0 {
		t.Error("the truncated upload must not be attempted")
	}
}

// zeroReader produces an endless stream of zero bytes, so a test can build a
// 20MB body without allocating one.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// routeServer answers both upload protocols and records which one was used.
type routeServer struct {
	mu       sync.Mutex
	block    int64
	direct   int
	prepare  int
	finished bool

	directBytes  int64
	chunkedBytes int64
	blocks       map[int]int
}

func newRouteServer(blockSize int64) *routeServer {
	return &routeServer{block: blockSize, blocks: map[int]int{}}
}

func (s *routeServer) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		s.mu.Lock()
		defer s.mu.Unlock()

		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/files"):
			// resolvePutParent lists the root; an empty folder keeps it simple.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "data": map[string]any{"files": []any{}, "has_more": false},
			})

		case strings.HasSuffix(r.URL.Path, "/files/upload_all"):
			s.direct++
			if err := r.ParseMultipartForm(64 << 20); err != nil {
				t.Errorf("parse multipart: %v", err)
				return
			}
			fh, _, err := r.FormFile("file")
			if err != nil {
				t.Errorf("missing file part: %v", err)
				return
			}
			defer func() { _ = fh.Close() }()
			data, err := io.ReadAll(fh)
			if err != nil {
				t.Errorf("read body: %v", err)
				return
			}
			s.directBytes = int64(len(data))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "data": map[string]any{"file_token": "direct-token"},
			})

		case strings.HasSuffix(r.URL.Path, "/files/upload_prepare"):
			s.prepare++
			var req struct {
				Size int64 `json:"size"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			num := int((req.Size + s.block - 1) / s.block)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"upload_id": "up-1", "block_size": s.block, "block_num": num,
				},
			})

		case strings.HasSuffix(r.URL.Path, "/files/upload_part"):
			if err := r.ParseMultipartForm(64 << 20); err != nil {
				t.Errorf("parse multipart: %v", err)
				return
			}
			seq, _ := strconv.Atoi(r.FormValue("seq"))
			fh, _, err := r.FormFile("file")
			if err != nil {
				t.Errorf("missing block: %v", err)
				return
			}
			defer func() { _ = fh.Close() }()
			data, err := io.ReadAll(fh)
			if err != nil {
				t.Errorf("read block: %v", err)
				return
			}
			s.blocks[seq] = len(data)
			s.chunkedBytes += int64(len(data))
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))

		case strings.HasSuffix(r.URL.Path, "/files/upload_finish"):
			s.finished = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "data": map[string]any{"file_token": "chunked-token"},
			})

		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}
}

// routeTestFs builds an Fs with a throwaway snapshot so Put can record uploads.
func routeTestFs(t *testing.T, srv *routeServer) *Fs {
	t.Helper()
	c, _ := testClient(t, srv.handler(t))

	const pairID = "0123456789abcdef"
	statePath := filepath.Join(t.TempDir(), pairID+".state.json")
	snap := state.New(pairID)
	snap.SetPath(statePath)

	f := &Fs{name: "test", client: c, st: snap, stPath: statePath, storable: true}
	f.resolver = newDirResolver(c, "root-token", "", "")
	return f
}
