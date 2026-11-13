package job

import (
	"context"
	"sync"
	"time"

	"github.com/rclone/rclone/fs/accounting"
	"github.com/rclone/rclone/fs/rc"
)

// Live progress for a run in flight.
//
// Why this exists at all: the transfers happen inside rclone's sync engine, so
// the only place the byte counters live is rclone's accounting. rclone supports
// named stats groups, so a run can be told to count into its own group instead of
// the global one. That is not a nicety: two jobs running at the same time would
// otherwise both report the sum of both, and "current file" would name a file
// belonging to the other job.
//
// The group name is stable per job rather than per run on purpose. rclone has no
// exported way to delete a group, so a fresh group per run would grow the map and
// leak one average-speed goroutine per run for the life of the process.

// Phase names what a run is doing right now.
//
// rclone's counters only move during transfers. Everything else a run spends time
// on - opening the remote, walking a folder of thousands of entries, comparing
// file contents byte by byte - happens with the byte counter standing still, so a
// long scan and a hung process look identical from the outside. Measured: a user
// watched "正在扫描并比较 已用 265 秒" with no further information and asked
// whether nothing at all is supposed to be displayed.
//
// This is not a progress percentage. It is the answer to "what is it doing",
// which is the question a frozen counter raises.
type Phase struct {
	// Kind is one of the Phase* constants below.
	Kind string `json:"kind"`
	// Pair is the folder pair being worked on.
	Pair string `json:"pair,omitempty"`
	// Done and Total count within the phase when it has a countable unit (files
	// compared, pairs finished). Total is 0 when unknown.
	Done  int `json:"done,omitempty"`
	Total int `json:"total,omitempty"`
}

// Phase kinds. Short machine tokens rather than sentences: the console translates
// them, the CLI prints them as-is, and a test can assert on them.
const (
	// PhaseOpen is opening the destination - on a cloud remote this is the first
	// network call of the run, and the first place it can stall.
	PhaseOpen = "open"
	// PhaseGuard is the pre-flight directory-size check.
	PhaseGuard = "guard"
	// PhaseConflict reads the cloud folder to compare it against the snapshot.
	PhaseConflict = "conflict"
	// PhaseScan is the comparison itself: listing both sides.
	PhaseScan = "scan"
	// PhaseContent is the byte-by-byte read-back used by compare = "content".
	// Nothing is transferred in this phase, so the counters do not move.
	PhaseContent = "content"
	// PhaseWrite is the actual transfer.
	PhaseWrite = "write"
	// PhaseFlush is writing the backend's snapshot to disk at the end.
	PhaseFlush = "flush"
	// PhaseVerify is the per-path existence check that turns "would create" into
	// "would update". It runs after the pass, makes one network call per planned
	// upload, and transfers nothing - measured: 4232 of them kept a preview busy
	// long after the byte counter had reached 100%.
	PhaseVerify = "verify"
)

// Progress is a snapshot of an in-flight run, shape-stable for JSON.
type Progress struct {
	// Phase is what the run is doing right now, if the caller asked to be told.
	//
	// This is the only part of Progress that moves during work that transfers
	// nothing (listing, comparison, content read-back); without it a long scan
	// looks exactly like a hung process.
	Phase *Phase `json:"phase,omitempty"`
	// Bytes is how much has been transferred in the current pass.
	Bytes int64 `json:"bytes"`
	// TotalBytes is the size of everything this pass intends to transfer. Zero
	// until the source has been listed, which is why ETA can be absent early.
	TotalBytes int64 `json:"total_bytes"`
	// Transfers counts finished files in the current pass.
	Transfers int64 `json:"transfers"`
	// Deletes counts removed items in the current pass.
	Deletes int64 `json:"deletes"`
	// Errors counts failed operations.
	Errors int64 `json:"errors"`
	// Speed is the average bytes/second for this pass.
	Speed float64 `json:"speed"`
	// ETA in seconds; nil when it cannot be estimated yet.
	ETA *float64 `json:"eta,omitempty"`
	// Files lists what is being transferred right now.
	Files []FileProgress `json:"files,omitempty"`
}

// FileProgress is one in-flight file.
type FileProgress struct {
	Name    string   `json:"name"`
	Bytes   int64    `json:"bytes"`
	Size    int64    `json:"size"`
	Percent int      `json:"percent"`
	Speed   float64  `json:"speed"`
	ETA     *float64 `json:"eta,omitempty"`
	Elapsed float64  `json:"elapsed,omitempty"`
}

// Empty reports whether nothing has happened yet.
func (p Progress) Empty() bool {
	return p.Bytes == 0 && p.Transfers == 0 && p.Deletes == 0 && len(p.Files) == 0
}

// Tracker owns one accounting group.
//
// The zero value is not usable; build one with NewTracker. A nil *Tracker is
// valid everywhere and reports emptiness, so callers do not need to branch.
type Tracker struct {
	group string
	stats *accounting.StatsInfo

	// resetAt is when the current pass started counting, used for the speed the UI
	// shows. It has its own mutex because Reset runs on the run goroutine while
	// Snapshot runs on an HTTP handler.
	mu      sync.Mutex
	resetAt time.Time
}

// NewTracker registers (or reuses) the accounting group called group.
//
// Reuse is required, not a nicety. accounting.NewStatsGroup always creates a new
// StatsInfo and starts a new average-speed goroutine; a second one for the same
// name would leave the transfers writing into one while the reader watches the
// other (and leak a goroutine per call). StatsGroup returns the existing entry
// when there is one.
func NewTracker(group string) *Tracker {
	stats := accounting.StatsGroup(context.Background(), group)
	return &Tracker{group: group, stats: stats}
}

// Group is the name to put in Options.StatsGroup.
func (t *Tracker) Group() string {
	if t == nil {
		return ""
	}
	return t.group
}

// Reset clears the counters at the start of a pass.
//
// Without this a second run would start from the previous run's totals and the UI
// would show a job that is already 100% done when it begins.
func (t *Tracker) Reset() {
	if t == nil || t.stats == nil {
		return
	}
	t.stats.ResetCounters()
	t.mu.Lock()
	t.resetAt = time.Now()
	t.mu.Unlock()
}

// Snapshot reads the current numbers.
//
// Everything is read from one RemoteStats call so the values cannot disagree with
// each other (bytes from one instant, current files from another).
func (t *Tracker) Snapshot() Progress {
	if t == nil || t.stats == nil {
		return Progress{}
	}
	raw, err := t.stats.RemoteStats(false)
	if err != nil || raw == nil {
		return Progress{}
	}

	p := Progress{
		Bytes:      asInt64(raw["bytes"]),
		TotalBytes: asInt64(raw["totalBytes"]),
		Transfers:  asInt64(raw["transfers"]),
		Deletes:    asInt64(raw["deletes"]),
		Errors:     asInt64(raw["errors"]),
		Speed:      asFloat64(raw["speed"]),
	}

	// rclone's "speed" only counts completed transfers, so a run that is busy with
	// four concurrent uploads reports 0 B/s for its whole duration - which is
	// exactly when the user is looking at it. Compute the rate from our own reset
	// time instead: bytes so far over time elapsed.
	//
	// This is an average since the pass started, not an instantaneous rate, so it
	// lags a ramp-up by design rather than jumping around.
	t.mu.Lock()
	started := t.resetAt
	t.mu.Unlock()
	if !started.IsZero() {
		if elapsed := time.Since(started).Seconds(); elapsed > 0.5 {
			if measured := float64(p.Bytes) / elapsed; measured > p.Speed {
				p.Speed = measured
			}
		}
	}

	// ETA is derived from that same rate, because rclone only publishes one once it
	// knows the total, and the total is unknown until the listing finishes.
	if p.Speed > 0 && p.TotalBytes > p.Bytes {
		remaining := float64(p.TotalBytes-p.Bytes) / p.Speed
		p.ETA = &remaining
	} else if v, ok := raw["eta"]; ok && v != nil {
		if f := asFloat64(v); f > 0 {
			p.ETA = &f
		}
	}

	for _, item := range asList(raw["transferring"]) {
		fp := FileProgress{
			Name:    asString(item["name"]),
			Bytes:   asInt64(item["bytes"]),
			Size:    asInt64(item["size"]),
			Percent: int(asInt64(item["percentage"])),
			Speed:   asFloat64(item["speed"]),
		}
		if v, ok := item["eta"]; ok && v != nil {
			if f := asFloat64(v); f > 0 {
				fp.ETA = &f
			}
		}
		p.Files = append(p.Files, fp)
	}
	return p
}

// The rclone stats payload is rc.Params (map[string]any). These helpers keep the
// type switches in one place instead of scattering them through the mapping code,
// and they return zero rather than panicking on a type the library changes.

func asInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case uint64:
		return int64(n)
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	}
	return 0
}

func asFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int64:
		return float64(n)
	case int:
		return float64(n)
	}
	return 0
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asList(v any) []map[string]any {
	// rclone returns []rc.Params here. A plain []any assertion does not match a
	// named slice type, which is exactly the kind of mistake that silently makes
	// "current file" always empty.
	switch items := v.(type) {
	case []rc.Params:
		out := make([]map[string]any, 0, len(items))
		for _, item := range items {
			out = append(out, map[string]any(item))
		}
		return out
	case []map[string]any:
		return items
	case []any:
		out := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// ETAString renders an ETA for humans; "" when unknown.
func ETAString(seconds *float64) string {
	if seconds == nil || *seconds <= 0 {
		return ""
	}
	d := time.Duration(*seconds * float64(time.Second)).Round(time.Second)
	return d.String()
}
