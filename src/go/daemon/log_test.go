package daemon

import (
	"io"
	"strings"
	"testing"
)

func logFixture(t *testing.T) *logSink {
	t.Helper()
	s := NewLogSink(io.Discard).(*logSink)
	_, _ = s.Write([]byte(
		"2026-11-07 10:00:00 job \"a\": starting\n" +
			"2026-11-07 10:00:01 job \"a\": failed after 1s: boom\n" +
			"2026-11-07 10:00:02 WARNING: guard hit, NOTHING was deleted\n" +
			"2026-11-07 10:00:03 job \"b\": finished in 2s\n" +
			"2026-11-07 10:00:04 DEBUG: rclone chatter\n"))
	// A line split across two writes must still be one line, and must not be
	// classified from its tail alone.
	_, _ = s.Write([]byte("2026-11-07 10:00:05 half a li"))
	_, _ = s.Write([]byte("ne with an error inside\n"))
	return s
}

// Lines must be split and timestamped exactly once.
func TestLogSinkSplitsAndStamps(t *testing.T) {
	s := logFixture(t)
	lines := s.Recent()
	if len(lines) != 6 {
		t.Fatalf("lines = %d, want 6\n%+v", len(lines), lines)
	}
	if lines[0].Time != "2026-11-07 10:00:00" {
		t.Errorf("time = %q", lines[0].Time)
	}
	if strings.Contains(lines[0].Text, "2026-11-07 10:00:00") {
		t.Error("the timestamp must be split out, not repeated inside the text")
	}
	if !strings.Contains(lines[5].Text, "half a line with an error inside") {
		t.Errorf("a line split across writes was mangled: %q", lines[5].Text)
	}
}

// Level is derived from the text; anything unrecognised stays info so no line can
// be filtered away by accident.
func TestLogSinkClassifiesLevels(t *testing.T) {
	s := logFixture(t)
	got := map[string]string{}
	for _, l := range s.Recent() {
		got[strings.TrimSpace(l.Text)] = l.Level
	}

	cases := []struct {
		text  string
		level string
	}{
		{`job "a": starting`, LogInfo},
		{`job "a": failed after 1s: boom`, LogError},
		{"WARNING: guard hit, NOTHING was deleted", LogWarning},
		{`job "b": finished in 2s`, LogInfo},
		{"DEBUG: rclone chatter", LogDebug},
		{"half a line with an error inside", LogError},
	}
	for _, c := range cases {
		if got[c.text] != c.level {
			t.Errorf("%q classified as %q, want %q", c.text, got[c.text], c.level)
		}
	}
}

// Filtering happens on the server, so each parameter has to work on its own.
func TestLogFiltered(t *testing.T) {
	s := logFixture(t)

	if n := len(s.Filtered(LogQuery{})); n != 6 {
		t.Errorf("no filter kept %d lines, want 6", n)
	}
	if n := len(s.Filtered(LogQuery{Level: LogError})); n != 2 {
		t.Errorf("level=error kept %d lines, want 2", n)
	}
	if n := len(s.Filtered(LogQuery{Level: LogWarning})); n != 1 {
		t.Errorf("level=warning kept %d lines, want 1", n)
	}
	if n := len(s.Filtered(LogQuery{Contains: "job"})); n != 3 {
		t.Errorf("contains=job kept %d lines, want 3", n)
	}

	// The search is case-insensitive: typing "warning" should find "WARNING".
	if n := len(s.Filtered(LogQuery{Contains: "warning"})); n != 1 {
		t.Errorf("case-insensitive search kept %d lines, want 1", n)
	}

	// Both together narrow rather than widen.
	if n := len(s.Filtered(LogQuery{Level: LogError, Contains: "boom"})); n != 1 {
		t.Errorf("level+contains kept %d lines, want 1", n)
	}

	// The limit keeps the tail, because "the last N" means the newest end.
	last := s.Filtered(LogQuery{Limit: 2})
	if len(last) != 2 {
		t.Fatalf("limit=2 kept %d lines", len(last))
	}
	if !strings.Contains(last[1].Text, "error inside") {
		t.Errorf("the limit dropped the newest lines: %+v", last)
	}
}

// Incremental polling must be able to ask for only what it has not seen.
func TestLogFilteredSince(t *testing.T) {
	s := logFixture(t)
	all := s.Recent()

	fresh := s.Filtered(LogQuery{Since: all[2].Seq})
	if len(fresh) != 3 {
		t.Errorf("since returned %d lines, want the 3 after seq %d", len(fresh), all[2].Seq)
	}
	if fresh[0].Seq <= all[2].Seq {
		t.Error("since returned a line that was already delivered")
	}
	if s.Seq() != all[len(all)-1].Seq {
		t.Errorf("Seq() = %d, want the newest line's %d", s.Seq(), all[len(all)-1].Seq)
	}
}

// The export must contain what the filter selected, and nothing else.
func TestLogDumpText(t *testing.T) {
	s := logFixture(t)

	body := s.DumpText(LogQuery{Level: LogError})
	if !strings.Contains(body, "boom") || !strings.Contains(body, "error inside") {
		t.Errorf("export is missing the errors:\n%s", body)
	}
	if strings.Contains(body, "starting") {
		t.Errorf("export ignored the filter:\n%s", body)
	}
	// One line per entry: a missing trailing newline makes the last line run into
	// whatever follows when the file is concatenated.
	if !strings.HasSuffix(body, "\n") {
		t.Error("the export should end with a newline")
	}
	if got := strings.Count(body, "\n"); got != 2 {
		t.Errorf("export has %d lines, want 2", got)
	}
}

// A line with no recognisable timestamp must still be kept.
func TestLogSinkKeepsUntimestampedLines(t *testing.T) {
	s := NewLogSink(io.Discard).(*logSink)
	_, _ = s.Write([]byte("weftsync daemon: API listening on http://127.0.0.1:5572\n"))

	lines := s.Recent()
	if len(lines) != 1 {
		t.Fatalf("lines = %d", len(lines))
	}
	if lines[0].Time != "" {
		t.Errorf("time = %q, want empty", lines[0].Time)
	}
	if lines[0].Level != LogInfo {
		t.Errorf("level = %q, want info", lines[0].Level)
	}
}
