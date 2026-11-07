// Package feishu implements an rclone backend for Feishu (Lark) Drive.
//
// Feishu Drive constraints discovered by M0 probing (see docs/feishu-probe.md):
//
//   - listing returns neither size nor content hash
//   - size CAN be recovered with a 1-byte Range request, at one request/file
//   - re-uploading the same name creates a NEW token; there is no in-place update
//   - duplicate names are allowed in one folder
//   - modified_time is second precision and equals the upload time
//   - upload_all is capped at 20MB; larger files need chunked upload
//   - the server decides the chunk size (4MB at probe time)
//   - deleting a file is irreversible through the API (no trash endpoint)
//
// The backend therefore declares Size() == -1 and no hashes, and relies on an
// external state file for incremental decisions. See
// specs/001-feishu-read-upload/ for the full contract set.
package feishu

import (
	"context"
	"os"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
)

// DefaultBaseURL is the public Feishu Open Platform endpoint.
const DefaultBaseURL = "https://open.feishu.cn"

// Rate limits advertised by Feishu's documentation.
//
// Nearly every endpoint allows 5 requests/second and 10000/day. The move
// endpoint is far tighter at 20 requests/minute. Both are applied by the client
// before a request goes out (see api.go), so the limits are avoided rather than
// recovered from.
const (
	defaultQPS           = 5.0
	defaultMovePerMinute = 20
)

// Environment variable names accepted as a fallback for the credentials.
const (
	EnvAppID     = "WEFTSYNC_FEISHU_APP_ID"
	EnvAppSecret = "WEFTSYNC_FEISHU_APP_SECRET"
)

// precision is the modification-time granularity advertised to rclone.
//
// Not fs.ModTimeNotSupported: that would make rclone treat every pair as equal
// and never re-upload local changes (verified with probes/e6_size_probe.go).
const precision = time.Second

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "feishu",
		Description: "Feishu / Lark Drive",
		NewFs:       NewFs, // Static so `backend help feishu:` can list them; the implementations live
		// on the Fs instance (see dedupe.go).
		CommandHelp: commandHelp(), Options: []fs.Option{
			{
				Name:     "app_id",
				Help:     "Feishu application ID.\n\nFalls back to the environment variable " + EnvAppID + " when empty.",
				Required: true,
			},
			{
				Name:       "app_secret",
				Help:       "Feishu application secret.\n\nFalls back to the environment variable " + EnvAppSecret + " when empty.",
				Required:   true,
				IsPassword: true,
			},
			{
				Name:     "root_token",
				Help:     "Folder token of the remote root.\n\nA full Feishu folder URL is also accepted and the token is extracted from it.",
				Required: true,
			},
			{
				Name: "state_file",
				Help: "Path to the incremental-sync state file.\n\n" +
					"Leave empty to derive it from the local and remote roots, which is the normal case.",
				Advanced: true,
			},
			{
				Name:     "base_url",
				Help:     "Base URL of the Feishu Open Platform API.",
				Default:  DefaultBaseURL,
				Advanced: true,
			},
			{
				Name: "qps",
				Help: "Maximum API requests per second.\n\n" +
					"Feishu limits nearly every endpoint to 5 requests/second and 10000/day; " +
					"exceeding it returns code 1061045. Requests are spaced out rather than " +
					"retried after the fact, so the limit is never hit in the first place.\n\n" +
					"Set to 0 to disable (only for testing).",
				Default:  defaultQPS,
				Advanced: true,
			},
			{
				Name: "move_per_minute",
				Help: "Maximum move requests per minute.\n\n" +
					"The move endpoint is limited far more tightly than the rest: 20 per minute. " +
					"Keeping it separate means a long list of renames cannot starve ordinary " +
					"uploads, and cannot trip the move limit.",
				Default:  defaultMovePerMinute,
				Advanced: true,
			},
		},
	})
}

// Options holds the parsed configuration for one feishu remote.
type Options struct {
	AppID         string  `config:"app_id"`
	AppSecret     string  `config:"app_secret"`
	RootToken     string  `config:"root_token"`
	StateFile     string  `config:"state_file"`
	BaseURL       string  `config:"base_url"`
	QPS           float64 `config:"qps"`
	MovePerMinute int     `config:"move_per_minute"`
}

// resolveEnv fills AppID / AppSecret from the environment when the config
// values are empty, then reports which required values are still missing.
//
// FR-001: a missing credential MUST fail at startup naming the missing item.
func (o *Options) resolveEnv() []string {
	if o.AppID == "" {
		o.AppID = os.Getenv(EnvAppID)
	}
	if o.AppSecret == "" {
		o.AppSecret = os.Getenv(EnvAppSecret)
	}

	var missing []string
	if o.AppID == "" {
		missing = append(missing, "app_id (or "+EnvAppID+")")
	}
	if o.AppSecret == "" {
		missing = append(missing, "app_secret (or "+EnvAppSecret+")")
	}
	if o.RootToken == "" {
		missing = append(missing, "root_token")
	}
	return missing
}

// NewFs constructs an Fs from the path.
//
// root is a logical path relative to root_token; an empty root means the
// root_token folder itself.
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	opt := new(Options)
	if err := configstruct.Set(m, opt); err != nil {
		return nil, err
	}
	if opt.BaseURL == "" {
		opt.BaseURL = DefaultBaseURL
	}

	f, err := newFs(ctx, name, root, opt)
	if err != nil {
		return nil, err
	}

	// rclone convention: when root resolves to an existing file, return an Fs
	// pointing at the parent together with fs.ErrorIsFile, so the caller
	// (e.g. `copy remote:dir/file.txt ./local`) retries with the parent.
	if parent, isFile := f.rootIsFile(ctx); isFile {
		f.root = parent
		f.resolver.rootPath = parent
		f.resolver.invalidateAll()
		return f, fs.ErrorIsFile
	}

	return f, nil
}
