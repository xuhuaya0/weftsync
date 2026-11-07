package daemon

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"time"

	"weftsync/job"
)

// History and log buffers.
//
// The UI needs to show what happened, not just what is happening. Both buffers are
// fixed-size rings: an unattended daemon may run for weeks, and an unbounded
// slice would grow until the process dies.

// RunRecord is one completed run, kept for the UI's history table.
type RunRecord struct {
	Job     string           `json:"job"`
	Started time.Time        `json:"started"`
	Ended   time.Time        `json:"ended"`
	OK      bool             `json:"ok"`
	Error   string           `json:"error,omitempty"`
	Results []job.PairResult `json:"results,omitempty"`
	// Conflicts is the number of conflicts reported across pairs.
	Conflicts int `json:"conflicts"`
	// Transferred and Deleted are totals across pairs.
	Transferred int64 `json:"transferred"`
	Deleted     int64 `json:"deleted"`
	GuardHit    bool  `json:"guard_hit"`
	// DryRun marks a preview: the numbers are what a run would have done.
	DryRun bool `json:"dry_run,omitempty"`
	// Stopped marks a run cut short on request; OK is true because nothing failed.
	Stopped bool `json:"stopped,omitempty"`
}

// historySize is how many runs are retained. Enough to answer "why did it fail
// last night" without keeping everything forever.
const historySize = 100

// logSize is how many log lines are retained for the UI.
const logSize = 500

// ring is a fixed-size circular buffer.
type ring[T any] struct {
	mu    sync.Mutex
	items []T
	next  int
	full  bool
}

func newRing[T any](size int) *ring[T] {
	return &ring[T]{items: make([]T, size)}
}

func (r *ring[T]) add(v T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[r.next] = v
	r.next = (r.next + 1) % len(r.items)
	if r.next == 0 {
		r.full = true
	}
}

// snapshot returns the contents oldest-first.
func (r *ring[T]) snapshot() []T {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := r.next
	if r.full {
		n = len(r.items)
	}
	out := make([]T, 0, n)
	if r.full {
		out = append(out, r.items[r.next:]...)
		out = append(out, r.items[:r.next]...)
		return out
	}
	out = append(out, r.items[:r.next]...)
	return out
}

// logSink records daemon log lines for the UI while forwarding them onward.
type logSink struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	lines *ring[LogLine]
	fwd   io.Writer
	seq   int64
}

// LogLine is one retained log line.
type LogLine struct {
	Time string `json:"time"`
	Text string `json:"text"`
	// Level is derived from the text, not supplied by the caller: rclone writes
	// to the same writer and does not tell us its severity, so the only option
	// that works for every producer is to read it out of the line.
	Level string `json:"level"`
	// Seq is a monotonically increasing number so the UI can ask for "everything
	// after what I already have" instead of downloading the buffer each poll.
	Seq int64 `json:"seq"`
}

// Log levels the UI can filter on.
const (
	LogInfo    = "info"
	LogWarning = "warning"
	LogError   = "error"
	LogDebug   = "debug"
)

// LogLineTimeLayout matches what the daemon and rclone put at the start of a line.
const LogLineTimeLayout = "2006-01-02 15:04:05"

// classifyLogLine splits "2026-11-07 20:00:00 rest" into a timestamp and level.
//
// Classification is by keyword because there is no structured logging to hook
// into: the daemon's own logf has no level, and rclone's output arrives as plain
// text. Being wrong here is cheap (a line lands one filter tab over) but silently
// dropping lines is not, so anything unrecognised stays "info" rather than being
// filtered away.
func classifyLogLine(raw string) LogLine {
	out := LogLine{Text: raw, Level: LogInfo}

	rest := raw
	if len(raw) > len(LogLineTimeLayout) && raw[len("2006-01-02")] == ' ' {
		if _, err := time.Parse(LogLineTimeLayout, raw[:len(LogLineTimeLayout)]); err == nil {
			out.Time = raw[:len(LogLineTimeLayout)]
			rest = raw[len(LogLineTimeLayout):]
			// The timestamp is moved into its own field, so it must come OUT of the
			// text: the UI renders the field and the text together, and leaving it
			// in produced two timestamps on every row (the same defect as the one
			// fixed when the sink stopped adding its own prefix).
			out.Text = strings.TrimSpace(rest)
		}
	}

	lower := strings.ToLower(rest)
	switch {
	case strings.Contains(lower, "error") || strings.Contains(lower, "failed") ||
		strings.Contains(lower, "失败") || strings.Contains(lower, "fatal") ||
		strings.Contains(lower, "panic"):
		out.Level = LogError
	case strings.Contains(lower, "warn") || strings.Contains(lower, "notice") ||
		strings.Contains(lower, "skipping") || strings.Contains(lower, "guard"):
		out.Level = LogWarning
	case strings.Contains(lower, "debug") || strings.Contains(lower, "trace"):
		out.Level = LogDebug
	}
	return out
}

// NewLogSink wraps a writer so the UI can show recent output. Passing the result
// as the daemon's log writer keeps the file/stdout sink and the UI sink in sync.
func NewLogSink(fwd io.Writer) io.Writer {
	return &logSink{lines: newRing[LogLine](logSize), fwd: fwd}
}

func (l *logSink) Write(p []byte) (int, error) {
	if l.fwd != nil {
		_, _ = l.fwd.Write(p)
	}

	l.mu.Lock()
	l.buf.Write(p)
	data := l.buf.String()
	var completed []string
	for {
		i := indexByte(data, '\n')
		if i < 0 {
			break
		}
		completed = append(completed, data[:i])
		data = data[i+1:]
	}
	l.buf.Reset()
	l.buf.WriteString(data)
	l.mu.Unlock()

	for _, line := range completed {
		if line == "" {
			continue
		}
		// No timestamp is added here: the daemon and rclone already stamp every
		// line, and prefixing again produced two timestamps per log row in the UI.
		entry := classifyLogLine(line)
		l.mu.Lock()
		l.seq++
		entry.Seq = l.seq
		l.mu.Unlock()
		l.lines.add(entry)
	}
	return len(p), nil
}

// Recent returns the retained log lines.
func (l *logSink) Recent() []LogLine { return l.lines.snapshot() }

// Seq is the sequence number of the newest retained line.
func (l *logSink) Seq() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

// LogQuery narrows the retained lines.
//
// Filtering happens here rather than in the browser because the buffer holds 500
// lines and the useful question is usually "show me the errors from this run",
// which should not require shipping everything first.
type LogQuery struct {
	// Level keeps only that level; empty keeps everything.
	Level string
	// Contains is a case-insensitive substring match.
	Contains string
	// Limit caps the result, newest kept. Zero means no cap.
	Limit int
	// Since returns only lines with Seq > Since, for incremental polling.
	Since int64
}

// Filtered returns the retained lines that match q, oldest first.
func (l *logSink) Filtered(q LogQuery) []LogLine {
	all := l.lines.snapshot()
	needle := strings.ToLower(strings.TrimSpace(q.Contains))

	out := make([]LogLine, 0, len(all))
	for _, line := range all {
		if q.Since > 0 && line.Seq <= q.Since {
			continue
		}
		if q.Level != "" && line.Level != q.Level {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(line.Text), needle) {
			continue
		}
		out = append(out, line)
	}

	// The limit keeps the tail: when asked for "the last N", the interesting end
	// is the newest, not the oldest.
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[len(out)-q.Limit:]
	}
	return out
}

// DumpText renders the retained lines as plain text for export.
//
// A download is what people do before sending a log to somebody else, so it has
// to be self-describing: the lines are already timestamped, and nothing extra is
// prepended that would have to be stripped again on the other end.
func (l *logSink) DumpText(q LogQuery) string {
	var b strings.Builder
	for _, line := range l.Filtered(q) {
		b.WriteString(line.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
