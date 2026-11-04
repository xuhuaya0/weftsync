// Package feishu implements an rclone backend for Feishu (Lark) Drive.
//
// M0 probing established these constraints (see docs/feishu-probe.md):
//
//   - listing returns no size and no hash
//   - size CAN be recovered with a 1-byte Range request
//   - re-uploading the same name creates a NEW token (no in-place update)
//   - duplicate names are allowed in one folder
//   - modified_time is second precision and changes on re-upload
//   - upload_all is capped at 20MB; larger files use chunked upload
//   - server decides chunk size (4MB as of the probe)
//
// This file starts as a stub so the build can be verified before the API
// layer is written.
package feishu

import (
	"context"
	"errors"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
)

// ErrNotImplemented is returned by every operation until the backend is built.
var ErrNotImplemented = errors.New("feishu: not implemented yet")

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "feishu",
		Description: "Feishu / Lark Drive",
		NewFs:       NewFs,
		Options:     []fs.Option{},
	})
}

// NewFs constructs an Fs from the path.
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	return nil, ErrNotImplemented
}
