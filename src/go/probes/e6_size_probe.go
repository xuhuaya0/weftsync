//go:build ignore

// E6 verification: how does rclone decide "same file" when the destination
// backend reports Size() == -1 and has no hashes?
//
// Run with:  go run e6_size_probe.go
//
// It calls operations.Equal directly with stub objects, so no real backend or
// network is involved.
package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fs/operations"
)

// ---------------------------------------------------------------- stub Fs

type stubFs struct {
	name      string
	precision time.Duration
}

func (f *stubFs) Name() string             { return f.name }
func (f *stubFs) Root() string             { return "" }
func (f *stubFs) String() string           { return f.name }
func (f *stubFs) Precision() time.Duration { return f.precision }
func (f *stubFs) Hashes() hash.Set         { return hash.Set(hash.None) }
func (f *stubFs) Features() *fs.Features   { return &fs.Features{} }

var _ fs.Info = (*stubFs)(nil)

// ------------------------------------------------------------ stub object

type stubObj struct {
	fs        fs.Info
	remote    string
	size      int64
	modTime   time.Time
	hashValue string
}

func (o *stubObj) Fs() fs.Info                           { return o.fs }
func (o *stubObj) String() string                        { return o.remote }
func (o *stubObj) Remote() string                        { return o.remote }
func (o *stubObj) Size() int64                           { return o.size }
func (o *stubObj) Storable() bool                        { return true }
func (o *stubObj) ModTime(ctx context.Context) time.Time { return o.modTime }
func (o *stubObj) Hash(ctx context.Context, ty hash.Type) (string, error) {
	return o.hashValue, nil
}

// fs.Object extras (unused by Equal, but required by the interface)
func (o *stubObj) SetModTime(ctx context.Context, t time.Time) error { return nil }
func (o *stubObj) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	return nil, fmt.Errorf("not implemented")
}
func (o *stubObj) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	return nil
}
func (o *stubObj) Remove(ctx context.Context) error { return nil }

var _ fs.Object = (*stubObj)(nil)

// ------------------------------------------------------------------- main

func main() {
	ctx := context.Background()
	base := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

	localFs := &stubFs{name: "local", precision: time.Nanosecond}
	// Feishu reports second precision, -1 size, no hash
	feishuSec := &stubFs{name: "feishu-1s", precision: time.Second}
	// Alternative declaration: backend cannot store modtime at all
	feishuNoMt := &stubFs{name: "feishu-nomt", precision: fs.ModTimeNotSupported}

	type tc struct {
		name   string
		local  *stubObj
		remote *stubObj
		expect string // what we expect: EQUAL or DIFFER
	}

	cases := []tc{
		{
			name:   "A. size=-1, 1s precision, same mtime",
			local:  &stubObj{fs: localFs, remote: "f.txt", size: 1000, modTime: base},
			remote: &stubObj{fs: feishuSec, remote: "f.txt", size: -1, modTime: base},
			expect: "EQUAL",
		},
		{
			name:   "B. size=-1, 1s precision, mtime differs by 1h",
			local:  &stubObj{fs: localFs, remote: "f.txt", size: 1000, modTime: base},
			remote: &stubObj{fs: feishuSec, remote: "f.txt", size: -1, modTime: base.Add(time.Hour)},
			expect: "DIFFER",
		},
		{
			name:   "C. size=-1, 1s precision, mtime differs by 500ms",
			local:  &stubObj{fs: localFs, remote: "f.txt", size: 1000, modTime: base},
			remote: &stubObj{fs: feishuSec, remote: "f.txt", size: -1, modTime: base.Add(500 * time.Millisecond)},
			expect: "EQUAL",
		},
		{
			name:   "D. size=-1, ModTimeNotSupported",
			local:  &stubObj{fs: localFs, remote: "f.txt", size: 1000, modTime: base},
			remote: &stubObj{fs: feishuNoMt, remote: "f.txt", size: -1, modTime: base.Add(5 * time.Hour)},
			expect: "EQUAL(never re-upload)",
		},
		{
			name:   "E. real size differs",
			local:  &stubObj{fs: localFs, remote: "f.txt", size: 1000, modTime: base},
			remote: &stubObj{fs: feishuSec, remote: "f.txt", size: 999, modTime: base},
			expect: "DIFFER",
		},
		{
			name:   "F. real size same, mtime same",
			local:  &stubObj{fs: localFs, remote: "f.txt", size: 1000, modTime: base},
			remote: &stubObj{fs: feishuSec, remote: "f.txt", size: 1000, modTime: base},
			expect: "EQUAL",
		},
		{
			name:   "G. real size same, mtime differs, NO hash available",
			local:  &stubObj{fs: localFs, remote: "f.txt", size: 1000, modTime: base},
			remote: &stubObj{fs: feishuSec, remote: "f.txt", size: 1000, modTime: base.Add(time.Hour)},
			expect: "DIFFER",
		},
	}

	fmt.Printf("%-46s %-10s %s\n", "case", "result", "verdict")
	fmt.Println("-------------------------------------------------------------------------------")
	for _, c := range cases {
		equal := operations.Equal(ctx, c.local, c.remote)
		got := "EQUAL"
		if !equal {
			got = "DIFFER"
		}
		verdict := "ok"
		if got != c.expect && !(c.expect == "EQUAL(never re-upload)" && equal) {
			verdict = "UNEXPECTED"
		}
		fmt.Printf("%-46s %-10s %s\n", c.name, got, verdict)
	}

	_ = configmap.Simple{}
}
