// Package plan turns a dry run into a per-path list of what would happen.
//
// Why a per-path list: the UI showed only totals ("transferred 3309"), which
// answers "how much" but not "what exactly", and the answer to "may I run this"
// is in the second question. FreeFileSync is built around that list, and the user
// asked for the same logic.
//
// The rows are not produced by a second comparison implementation. They are read
// back out of rclone's own dry-run logging:
//
//	operations.SkipDestructive calls
//	  fs.Logf(subject, "Skipped %s as %s is set", LogValue("skipped", action), flag)
//
// so every row is what rclone itself decided, and the plan cannot disagree with
// the run that follows it. A plan built from a parallel implementation would
// drift, and a preview that lies is worse than no preview: it teaches the user to
// stop reading it.
package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/rclone/rclone/fs"
	rcloneLog "github.com/rclone/rclone/fs/log"
)

// Action is what would happen to one path.
type Action string

const (
	// Create uploads a path the destination does not have.
	Create Action = "create"
	// Update uploads a path the destination has, because the local copy changed.
	Update Action = "update"
	// Download brings a remote-only change down (two-way only).
	Download Action = "download"
	// DeleteRemote removes the cloud copy.
	DeleteRemote Action = "delete-remote"
	// DeleteLocal removes the local copy (two-way with propagation on).
	DeleteLocal Action = "delete-local"
	// Mkdir creates a destination directory.
	Mkdir Action = "mkdir"
	// Rmdir removes a destination directory.
	Rmdir Action = "rmdir"
	// Touch updates a modification time without moving data. rclone plans this for
	// a file whose content matches but whose timestamp differs (measured: two
	// identical files written a moment apart produce one row each). Kept as its own
	// action because it is neither "no change" nor a transfer, and lumping it in
	// with Other would leave the user unable to filter the noise out.
	Touch Action = "touch"
	// Other is something rclone plans that this package does not classify, such as
	// a modification-time update. Kept rather than dropped: a row that shows up as
	// "other" is a prompt to classify it, whereas a silently dropped row makes the
	// totals wrong.
	Other Action = "other"
)

// Row is one planned action.
type Row struct {
	Path   string `json:"path"`
	Action Action `json:"action"`
	// Size is the object size in bytes when rclone reported one, 0 otherwise.
	Size int64 `json:"size,omitempty"`
	// Detail is rclone's own wording for the action, kept for the rows this
	// package maps to Other so the UI can still say something specific.
	Detail string `json:"detail,omitempty"`
}

// Summary counts the rows by action.
type Summary struct {
	Create       int   `json:"create"`
	Update       int   `json:"update"`
	Download     int   `json:"download"`
	DeleteRemote int   `json:"delete_remote"`
	DeleteLocal  int   `json:"delete_local"`
	Mkdir        int   `json:"mkdir"`
	Rmdir        int   `json:"rmdir"`
	Touch        int   `json:"touch"`
	Other        int   `json:"other"`
	Bytes        int64 `json:"bytes"`
}

// Summarise counts a row list.
func Summarise(rows []Row) Summary {
	var s Summary
	for _, r := range rows {
		switch r.Action {
		case Create:
			s.Create++
		case Update:
			s.Update++
		case Download:
			s.Download++
		case DeleteRemote:
			s.DeleteRemote++
		case DeleteLocal:
			s.DeleteLocal++
		case Mkdir:
			s.Mkdir++
		case Rmdir:
			s.Rmdir++
		case Touch:
			s.Touch++
		default:
			s.Other++
		}
		if r.Action != DeleteRemote && r.Action != DeleteLocal &&
			r.Action != Mkdir && r.Action != Rmdir && r.Action != Touch {
			s.Bytes += r.Size
		}
	}
	return s
}

// Empty reports whether no file content would change.
//
// Metadata-only rows (Touch) are deliberately ignored: "this pair is in sync" is
// the answer the user needs, and a timestamp difference would otherwise make an
// already-synced pair never look synced. The count is still available for the UI
// to show.
func (s Summary) Empty() bool {
	return s.Create == 0 && s.Update == 0 && s.Download == 0 &&
		s.DeleteRemote == 0 && s.DeleteLocal == 0 && s.Mkdir == 0 && s.Rmdir == 0
}

// ErrBusy means another collection is already running in this process.
//
// Collection is process-wide because rclone's log hook is: the hook sees every
// log line from every goroutine, and log lines carry no job identity. Two
// concurrent plans would interleave into each other's rows, so the second one is
// refused instead of producing a plausible-looking mixture.
var ErrBusy = errors.New("plan: another preview is already collecting")

// Collector accumulates rows from the dry run it is installed around.
type Collector struct {
	srcType string
	dstType string

	mu   sync.Mutex
	rows []Row
	// seen deduplicates (action, path). A mirror preview whose byte ceiling trips
	// re-measures with the ceiling off, so the same skip is logged twice; without
	// this the plan would list every deletion twice while the counters beside it
	// said once.
	seen map[string]bool
}

var (
	installOnce sync.Once
	activeMu    sync.Mutex
	active      *Collector
)

// Begin installs the collector and returns it.
//
// The source and destination types (fmt.Sprintf("%T", f)) are how a log line is
// attributed to a side: rclone's dry-run lines carry the object's type, so
// nothing has to parse a path to know whether a copy goes up or comes down.
func Begin(src, dst fs.Fs) (*Collector, error) {
	activeMu.Lock()
	defer activeMu.Unlock()
	if active != nil {
		return nil, ErrBusy
	}

	installOnce.Do(func() {
		// JSON, because the fields this needs ("object", "skipped", "size") are
		// structured attributes, not part of the message text.
		rcloneLog.Handler.AddOutput(true, dispatch)
	})

	active = &Collector{
		srcType: typeName(src),
		dstType: typeName(dst),
		seen:    make(map[string]bool),
	}
	return active, nil
}

func typeName(f fs.Fs) string {
	if f == nil {
		return ""
	}
	return fmt.Sprintf("%T", f)
}

// dispatch forwards a log line to the active collector, if any.
func dispatch(_ slog.Level, text string) {
	activeMu.Lock()
	c := active
	activeMu.Unlock()
	if c == nil {
		return
	}
	c.consume(text)
}

// End uninstalls the collector and returns the rows in the order they occurred.
func (c *Collector) End() []Row {
	activeMu.Lock()
	if active == c {
		active = nil
	}
	activeMu.Unlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	rows := make([]Row, len(c.rows))
	copy(rows, c.rows)
	return rows
}

// Rows returns what has been collected so far. Used by tests and for progress.
func (c *Collector) Rows() []Row {
	c.mu.Lock()
	defer c.mu.Unlock()
	rows := make([]Row, len(c.rows))
	copy(rows, c.rows)
	return rows
}

// logLine is the subset of rclone's JSON log format this package reads.
//
// "object" and "objectType" come from fs.logSlogWithObject; "skipped" and "size"
// come from the LogValue arguments of operations.SkipDestructive.
type logLine struct {
	Msg        string          `json:"msg"`
	Object     string          `json:"object"`
	ObjectType string          `json:"objectType"`
	Skipped    string          `json:"skipped"`
	Size       json.RawMessage `json:"size"`
}

func (c *Collector) consume(text string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "{") {
		return // not JSON: the text format carries no structured fields
	}
	var line logLine
	if err := json.Unmarshal([]byte(text), &line); err != nil {
		return
	}
	if line.Skipped == "" {
		return // not a dry-run skip: ordinary log traffic
	}

	action := c.classify(line)
	if action == "" {
		return
	}
	row := Row{
		Path:   cleanPath(line.Object),
		Action: action,
		Size:   parseSize(line.Size, line.Msg),
	}
	if action == Other {
		row.Detail = line.Skipped
	}
	if row.Path == "" {
		return // a log line about the root: nothing to show per-path
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	key := string(row.Action) + "\x00" + row.Path
	if c.seen == nil {
		c.seen = make(map[string]bool)
	}
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	c.rows = append(c.rows, row)
}

// classify maps rclone's action word plus the object's side to an Action.
//
// Side matters because rclone logs the same "copy" for an upload and for a
// download: SkipDestructive is called with the *source* object, so comparing the
// logged type against the two Fs types tells them apart without guessing from the
// path or the job variant.
func (c *Collector) classify(line logLine) Action {
	toDst := c.dstType != "" && line.ObjectType == c.dstType
	toSrc := c.srcType != "" && line.ObjectType == c.srcType

	switch line.Skipped {
	case "copy":
		switch {
		case toSrc:
			return Create
		case toDst:
			// Copying into the source side, i.e. a download.
			return Download
		default:
			// Unknown side (a wrapped Fs, or a backend that logs its own type).
			// Still a copy; the caller knows which pass it ran and can relabel.
			return Create
		}
	case "delete":
		switch {
		case toDst:
			return DeleteRemote
		case toSrc:
			return DeleteLocal
		default:
			return DeleteRemote
		}
	case "make directory":
		return Mkdir
	case "remove directory", "purge directory":
		return Rmdir
	case "update modification time", "touch",
		"update directory metadata", "set directory modification time":
		// The two directory forms show up on every directory in a real preview
		// (measured: a 2-directory job produced 2 rows). They are metadata-only and
		// belong with the file timestamps, not in "other", where they made the
		// summary claim two unexplained actions.
		return Touch
	case "check":
		// `check` in dry mode reports differences without changing anything.
		return ""
	default:
		return Other
	}
}

// cleanPath normalises the logged object path.
func cleanPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "/")
	return p
}

// parseSize reads the size field, which rclone may render as a number or, for
// fs.SizeSuffix, as a human string ("1.234Ki"). The message text carries the same
// value, so a string form can still be recovered.
func parseSize(raw json.RawMessage, msg string) int64 {
	if len(raw) > 0 {
		var n int64
		if err := json.Unmarshal(raw, &n); err == nil {
			return n
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			if v, err := parseSizeText(s); err == nil {
				return v
			}
		}
	}
	// Fall back to "(size 1.234Ki)" in the message.
	const marker = "(size "
	if i := strings.LastIndex(msg, marker); i >= 0 {
		rest := msg[i+len(marker):]
		if j := strings.IndexByte(rest, ')'); j > 0 {
			if v, err := parseSizeText(rest[:j]); err == nil {
				return v
			}
		}
	}
	return 0
}

// parseSizeText parses "1234" or an rclone size suffix such as "1.5Mi".
func parseSizeText(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty size")
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	var sz fs.SizeSuffix
	if err := sz.Set(s); err != nil {
		return 0, err
	}
	return int64(sz), nil
}

// ApplySnapshot returns a copy of rows with the paths the sync state already
// knows relabelled from Create to Update.
//
// A copy of a path in the snapshot is an update, not a new file, and the UI asks
// for that distinction because it decides how much attention a row deserves.
// rclone's log does not carry it (it logs "copy" either way), so the caller
// supplies the answer from the same snapshot the sync itself uses.
//
// known is a function rather than a map so a 74k-entry snapshot is not turned
// into a second copy just to label rows.
//
// The input is not modified: an in-place version bit its own test, where a second
// call saw the relabelled result of the first and concluded the relabelling was
// unconditional.
func ApplySnapshot(rows []Row, known func(path string) bool) []Row {
	if known == nil {
		return rows
	}
	out := make([]Row, len(rows))
	copy(out, rows)
	for i := range out {
		if out[i].Action == Create && known(out[i].Path) {
			out[i].Action = Update
		}
	}
	return out
}
