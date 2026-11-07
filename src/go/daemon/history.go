package daemon

import (
	"bytes"
	"io"
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
}

// LogLine is one retained log line.
type LogLine struct {
	Time string `json:"time"`
	Text string `json:"text"`
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
		l.lines.add(LogLine{Text: line})
	}
	return len(p), nil
}

// Recent returns the retained log lines.
func (l *logSink) Recent() []LogLine { return l.lines.snapshot() }

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
