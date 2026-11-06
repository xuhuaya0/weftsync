package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// T021: path normalisation, token extraction, resolver caching, duplicate
// collapsing.

func TestExtractFolderToken(t *testing.T) {
	cases := []struct{ in, want string }{
		{"KARRfvoYtlX9T3dOoaTcmdELnyv", "KARRfvoYtlX9T3dOoaTcmdELnyv"},
		{"https://jingluesemi.feishu.cn/drive/folder/KARRfvoYtlX9T3dOoaTcmdELnyv", "KARRfvoYtlX9T3dOoaTcmdELnyv"},
		{"https://jingluesemi.feishu.cn/drive/folder/KARRfvoYtlX9T3dOoaTcmdELnyv?from=space", "KARRfvoYtlX9T3dOoaTcmdELnyv"},
		{"https://x.feishu.cn/drive/folder/ABC123#frag", "ABC123"},
		{"  spaced-token  ", "spaced-token"},
		{"", ""},
	}
	for _, c := range cases {
		if got := ExtractFolderToken(c.in); got != c.want {
			t.Errorf("ExtractFolderToken(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a/b/c", "a/b/c"},
		{`a\b\c`, "a/b/c"},
		{"/leading", "leading"},
		{"trailing/", "trailing"},
		{"//double//slash//", "double/slash"},
		{"", ""},
		{"季度报表 2024.xlsx", "季度报表 2024.xlsx"},
		{"CaseSensitive", "CaseSensitive"}, // case preserved: Feishu is case sensitive
	}
	for _, c := range cases {
		if got := normalizePath(c.in); got != c.want {
			t.Errorf("normalizePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParentOf(t *testing.T) {
	cases := []struct{ in, p, n string }{
		{"a/b/c.txt", "a/b", "c.txt"},
		{"c.txt", "", "c.txt"},
		{"/a/b/", "a", "b"},
	}
	for _, c := range cases {
		p, n := parentOf(c.in)
		if p != c.p || n != c.n {
			t.Errorf("parentOf(%q) = (%q,%q), want (%q,%q)", c.in, p, n, c.p, c.n)
		}
	}
}

func TestJoinPath(t *testing.T) {
	cases := []struct{ base, name, want string }{
		{"", "a", "a"},
		{"a", "", "a"},
		{"a", "b", "a/b"},
		{"a/", "/b", "a/b"},
	}
	for _, c := range cases {
		if got := joinPath(c.base, c.name); got != c.want {
			t.Errorf("joinPath(%q,%q) = %q, want %q", c.base, c.name, got, c.want)
		}
	}
}

// resolveDir must walk the tree, and repeated calls must reuse the cache.
func TestResolveDirUsesCache(t *testing.T) {
	var listCalls int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&listCalls, 1)
		folder := r.URL.Query().Get("folder_token")
		var files []map[string]string
		switch folder {
		case "ROOT":
			files = []map[string]string{
				{"name": "docs", "token": "DOCS", "type": "folder", "created_time": "100", "modified_time": "100"},
			}
		case "DOCS":
			files = []map[string]string{
				{"name": "2024", "token": "Y2024", "type": "folder", "created_time": "100", "modified_time": "100"},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "data": map[string]any{"files": files, "has_more": false},
		})
	})

	r := newDirResolver(c, "ROOT", "", "")

	got, err := r.resolveDir(context.Background(), "docs/2024")
	if err != nil {
		t.Fatalf("resolveDir: %v", err)
	}
	if got != "Y2024" {
		t.Errorf("token = %q, want Y2024", got)
	}

	before := atomic.LoadInt32(&listCalls)
	if _, err := r.resolveDir(context.Background(), "docs/2024"); err != nil {
		t.Fatalf("second resolveDir: %v", err)
	}
	if after := atomic.LoadInt32(&listCalls); after != before {
		t.Errorf("second resolve re-listed the tree (%d -> %d calls); cache not used", before, after)
	}
}

func TestResolveDirNotFound(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "data": map[string]any{"files": []any{}, "has_more": false},
		})
	})
	r := newDirResolver(c, "ROOT", "", "")

	_, err := r.resolveDir(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected an error for a missing directory")
	}
	if !isNotFound(err) {
		t.Errorf("error should be classified as not-found, got: %v", err)
	}
}

func TestResolveRootIsFree(t *testing.T) {
	var calls int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	})
	r := newDirResolver(c, "ROOT", "", "")

	tok, err := r.resolveDir(context.Background(), "")
	if err != nil {
		t.Fatalf("resolveDir(\"\"): %v", err)
	}
	if tok != "ROOT" {
		t.Errorf("token = %q, want ROOT", tok)
	}
	if calls != 0 {
		t.Errorf("resolving the root made %d API calls, want 0", calls)
	}
}

// C3: duplicate names collapse to one entry, preferring the recorded token.
func TestPickRemoteEntry(t *testing.T) {
	old := remoteEntry{Name: "f.txt", Token: "OLD", CreatedTime: 100}
	newer := remoteEntry{Name: "f.txt", Token: "NEW", CreatedTime: 200}

	// Without a preferred token the newest wins.
	if got := pickRemoteEntry([]remoteEntry{old, newer}, ""); got.Token != "NEW" {
		t.Errorf("expected NEW (most recent), got %s", got.Token)
	}
	// With a preferred token that token wins regardless of age.
	if got := pickRemoteEntry([]remoteEntry{old, newer}, "OLD"); got.Token != "OLD" {
		t.Errorf("expected OLD (preferred), got %s", got.Token)
	}
	// A preferred token that no longer exists falls back to newest.
	if got := pickRemoteEntry([]remoteEntry{old, newer}, "GONE"); got.Token != "NEW" {
		t.Errorf("expected NEW fallback, got %s", got.Token)
	}
}

// listFolder must follow pagination and stop at has_more=false.
func TestListFolderPaginates(t *testing.T) {
	var pages int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&pages, 1)
		var files []map[string]string
		for i := 0; i < 3; i++ {
			files = append(files, map[string]string{
				"name":          fmt.Sprintf("f%d-%d", n, i),
				"token":         fmt.Sprintf("T%d%d", n, i),
				"type":          "file",
				"created_time":  "100",
				"modified_time": "100",
			})
		}
		resp := map[string]any{
			"code": 0,
			"data": map[string]any{"files": files, "has_more": n < 2},
		}
		if n < 2 {
			resp["data"].(map[string]any)["next_page_token"] = "cursor2"
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	entries, err := c.listFolder(context.Background(), "ROOT")
	if err != nil {
		t.Fatalf("listFolder: %v", err)
	}
	if len(entries) != 6 {
		t.Errorf("entries = %d, want 6 (2 pages x 3)", len(entries))
	}
	if got := atomic.LoadInt32(&pages); got != 2 {
		t.Errorf("pages = %d, want 2", got)
	}
}

// A folder above the guard limit must fail loudly rather than loop.
func TestListFolderRejectsHugeFolders(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		files := make([]map[string]string, 0, 300)
		for i := 0; i < 300; i++ {
			files = append(files, map[string]string{
				"name": fmt.Sprintf("f%d", i), "token": fmt.Sprintf("T%d", i),
				"type": "file", "created_time": "1", "modified_time": "1",
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"files": files, "has_more": true, "next_page_token": "more"},
		})
	})

	_, err := c.listFolder(context.Background(), "ROOT")
	if err == nil {
		t.Fatal("expected the folder size guard to trigger")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeTooManyNodes {
		t.Errorf("error = %v, want CodeTooManyNodes", err)
	}
}

// parse timestamps that arrive as strings
func TestParseInt64(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"1793783897", 1793783897},
		{"", 0},
		{"12a4", 0},
		{"0", 0},
	}
	for _, c := range cases {
		if got := parseInt64(c.in); got != c.want {
			t.Errorf("parseInt64(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// buildURL sanity: query values are escaped, not concatenated raw.
func TestQueryEscaping(t *testing.T) {
	q := url.Values{}
	q.Set("folder_token", "a b&c")
	if !strings.Contains(q.Encode(), "a+b%26c") {
		t.Errorf("query not escaped: %s", q.Encode())
	}
}
