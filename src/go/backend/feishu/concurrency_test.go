package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
)

// Concurrent callers must share the resolver cache without losing entries or
// deadlocking.
//
// Note: `go test -race` cannot run here because the toolchain needs a C
// compiler for cgo, which this machine lacks. This test still exercises the
// mutex paths and would fail on a deadlock or a lost update.
func TestResolverConcurrentAccess(t *testing.T) {
	var listCalls int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&listCalls, 1)
		files := []map[string]string{
			{"name": "docs", "token": "DOCS", "type": "folder", "created_time": "1", "modified_time": "1"},
			{"name": "a.txt", "token": "A", "type": "file", "created_time": "1", "modified_time": "1"},
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "data": map[string]any{"files": files, "has_more": false},
		})
	})

	r := newDirResolver(c, "ROOT", "", "")

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				tok, err := r.resolveDir(context.Background(), "docs")
				if err != nil {
					errs <- fmt.Errorf("worker %d: %w", n, err)
					return
				}
				if tok != "DOCS" {
					errs <- fmt.Errorf("worker %d: token = %q, want DOCS", n, tok)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	// With a working cache the tree is walked once or twice (races at the very
	// start may both miss), never 16*8 times.
	if got := atomic.LoadInt32(&listCalls); got > 4 {
		t.Errorf("root listed %d times; cache is not effective under concurrency", got)
	}
}

// The resolver must survive a cache invalidation mid-flight.
func TestResolverInvalidateDuringUse(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"files": []map[string]string{
					{"name": "d", "token": "D", "type": "folder", "created_time": "1", "modified_time": "1"},
				},
				"has_more": false,
			},
		})
	})

	r := newDirResolver(c, "ROOT", "", "")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if _, err := r.resolveDir(context.Background(), "d"); err != nil {
					t.Errorf("resolveDir: %v", err)
					return
				}
				r.invalidate("ROOT")
			}
		}()
	}
	wg.Wait()
}
