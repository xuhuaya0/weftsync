package plan

import (
	"errors"
	"strings"
	"testing"
)

// line builds a log line in rclone's JSON format.
func line(fields map[string]string) string {
	parts := make([]string, 0, len(fields))
	for k, v := range fields {
		parts = append(parts, `"`+k+`":"`+v+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func newCollector() *Collector {
	return &Collector{srcType: "*local.Fs", dstType: "*feishu.Fs"}
}

// A copy of an object from the source side is an upload. This is the ordinary
// case and the one the UI shows most often.
func TestCopyFromSourceIsCreate(t *testing.T) {
	c := newCollector()
	c.consume(line(map[string]string{
		"level": "notice", "msg": "Skipped copy as --dry-run is set",
		"object": "a/b.txt", "objectType": "*local.Fs", "skipped": "copy",
	}))
	rows := c.Rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Action != Create || rows[0].Path != "a/b.txt" {
		t.Fatalf("row = %+v, want create a/b.txt", rows[0])
	}
}

// A copy whose logged object lives on the destination side is a download.
// rclone logs "copy" for both, so the side is what tells them apart: getting this
// wrong would show every two-way pull as an upload.
func TestCopyFromDestinationIsDownload(t *testing.T) {
	c := newCollector()
	c.consume(line(map[string]string{
		"msg":    "Skipped copy as --dry-run is set",
		"object": "x/y.txt", "objectType": "*feishu.Fs", "skipped": "copy",
	}))
	rows := c.Rows()
	if len(rows) != 1 || rows[0].Action != Download {
		t.Fatalf("rows = %+v, want one download", rows)
	}
}

// Deletions are attributed to the side the object belongs to.
func TestDeleteIsAttributedToItsSide(t *testing.T) {
	c := newCollector()
	c.consume(line(map[string]string{
		"msg":    "Skipped delete as --dry-run is set",
		"object": "cloud-only.txt", "objectType": "*feishu.Fs", "skipped": "delete",
	}))
	c.consume(line(map[string]string{
		"msg":    "Skipped delete as --dry-run is set",
		"object": "local-only.txt", "objectType": "*local.Fs", "skipped": "delete",
	}))
	rows := c.Rows()
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want 2", rows)
	}
	if rows[0].Action != DeleteRemote || rows[1].Action != DeleteLocal {
		t.Fatalf("actions = %v/%v, want delete-remote/delete-local",
			rows[0].Action, rows[1].Action)
	}
}

// Directory actions are separate from file actions so the totals can say
// "3 files, 2 directories" instead of lumping them together.
func TestDirectoryActions(t *testing.T) {
	c := newCollector()
	c.consume(line(map[string]string{
		"msg":    "Skipped make directory as --dry-run is set",
		"object": "newdir", "objectType": "*feishu.Fs", "skipped": "make directory",
	}))
	c.consume(line(map[string]string{
		"msg":    "Skipped remove directory as --dry-run is set",
		"object": "olddir", "objectType": "*feishu.Fs", "skipped": "remove directory",
	}))
	rows := c.Rows()
	if len(rows) != 2 || rows[0].Action != Mkdir || rows[1].Action != Rmdir {
		t.Fatalf("rows = %+v, want mkdir then rmdir", rows)
	}
}

// Modification-time updates are their own action, not Other: rclone plans them for
// files whose content already matches, so they would otherwise drown the list.
func TestModificationTimeIsTouch(t *testing.T) {
	c := newCollector()
	c.consume(line(map[string]string{
		"msg":    "Skipped update modification time as --dry-run is set",
		"object": "stamp.txt", "objectType": "*feishu.Fs",
		"skipped": "update modification time",
	}))
	rows := c.Rows()
	if len(rows) != 1 || rows[0].Action != Touch {
		t.Fatalf("rows = %+v, want one touch", rows)
	}
}

// An unrecognised skip is still kept, with rclone's wording, so the totals cannot
// quietly disagree with the run.
func TestUnclassifiedActionsAreKept(t *testing.T) {
	c := newCollector()
	c.consume(line(map[string]string{
		"msg":    "Skipped rename to other.txt as --dry-run is set",
		"object": "stamp.txt", "objectType": "*feishu.Fs",
		"skipped": "rename to other.txt",
	}))
	rows := c.Rows()
	if len(rows) != 1 || rows[0].Action != Other {
		t.Fatalf("rows = %+v, want one Other", rows)
	}
	if rows[0].Detail != "rename to other.txt" {
		t.Errorf("detail = %q, want rclone's own wording", rows[0].Detail)
	}
}

// Everything that is not a dry-run skip must be ignored, or ordinary log traffic
// would turn into phantom rows.
func TestNonSkipLinesAreIgnored(t *testing.T) {
	c := newCollector()
	c.consume("NOTICE: plain text output, not JSON")
	c.consume(`{"level":"notice","msg":"Copied (new)","object":"a.txt"}`)
	c.consume(`{not valid json`)
	c.consume("")
	if rows := c.Rows(); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
}

// Sizes arrive either as a JSON number or as rclone's human string; both must
// work, and the message text is the last resort.
func TestSizeParsing(t *testing.T) {
	cases := []struct {
		name string
		line string
		want int64
	}{
		{"number", `{"msg":"m","object":"a","objectType":"*local.Fs","skipped":"copy","size":1234}`, 1234},
		{"human string", `{"msg":"m","object":"a","objectType":"*local.Fs","skipped":"copy","size":"1.5Ki"}`, 1536},
		{"in message", `{"msg":"Skipped copy as --dry-run is set (size 2Ki)","object":"a","objectType":"*local.Fs","skipped":"copy"}`, 2048},
		{"absent", `{"msg":"m","object":"a","objectType":"*local.Fs","skipped":"copy"}`, 0},
	}
	for _, tc := range cases {
		c := newCollector()
		c.consume(tc.line)
		rows := c.Rows()
		if len(rows) != 1 {
			t.Fatalf("%s: rows = %+v", tc.name, rows)
		}
		if rows[0].Size != tc.want {
			t.Errorf("%s: size = %d, want %d", tc.name, rows[0].Size, tc.want)
		}
	}
}

// Summarise has to match the rows exactly; a preview whose header disagrees with
// its own list is worse than no header.
func TestSummarise(t *testing.T) {
	rows := []Row{
		{Action: Create, Size: 100},
		{Action: Create, Size: 200},
		{Action: Update, Size: 300},
		{Action: Download, Size: 400},
		{Action: DeleteRemote, Size: 999}, // deletions do not count as bytes moved
		{Action: Mkdir},
		{Action: Touch, Size: 50}, // metadata only
		{Action: Other, Size: 50},
	}
	s := Summarise(rows)
	if s.Create != 2 || s.Update != 1 || s.Download != 1 ||
		s.DeleteRemote != 1 || s.Mkdir != 1 || s.Touch != 1 || s.Other != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if s.Bytes != 100+200+300+400+50 {
		t.Errorf("bytes = %d, want the sum of content rows only", s.Bytes)
	}
	if s.Empty() {
		t.Error("Empty must be false when there is work")
	}
	if !Summarise(nil).Empty() {
		t.Error("an empty row list must report Empty")
	}
	// Metadata-only differences must not make a synced pair look unsynced.
	if !Summarise([]Row{{Action: Touch}}).Empty() {
		t.Error("touch-only rows must count as in sync")
	}
}

// ApplySnapshot must relabel only creates, and only when the snapshot knows the
// path: a copy of a file the snapshot never saw is still a create.
func TestApplySnapshotRelabelsCreatesOnly(t *testing.T) {
	rows := []Row{
		{Path: "known.txt", Action: Create, HasLeft: true},
		{Path: "new.txt", Action: Create, HasLeft: true},
		{Path: "known.txt", Action: Download, HasRight: true},
		{Path: "known.txt", Action: DeleteRemote, HasRight: true},
	}
	known := func(p string) (int64, bool) {
		if p == "known.txt" {
			return 4242, true
		}
		return 0, false
	}
	got := ApplySnapshot(rows, known)

	if got[0].Action != Update {
		t.Errorf("a create of a known path should become an update, got %v", got[0].Action)
	}
	if got[1].Action != Create {
		t.Errorf("a create of an unknown path must stay a create, got %v", got[1].Action)
	}
	if got[2].Action != Download || got[3].Action != DeleteRemote {
		t.Errorf("non-create actions must not be relabelled: %v/%v", got[2].Action, got[3].Action)
	}

	// The snapshot's size belongs in the destination pane, and only there: the
	// upload row is the one that has a destination copy to describe.
	if got[0].RightSize != 4242 {
		t.Errorf("update row destination size = %d, want the snapshot's 4242",
			got[0].RightSize)
	}
	if got[1].RightSize != 0 {
		t.Errorf("a create must not claim a destination size, got %d", got[1].RightSize)
	}

	// A nil lookup leaves everything alone rather than guessing.
	if again := ApplySnapshot(rows, nil); again[0].Action != Create {
		t.Errorf("nil lookup relabelled a row")
	}
}

// setSides has to describe where each action's file actually lives; a wrong side
// prints a size in an empty cell, which reads as "this file exists there".
func TestSetSidesMatchesTheAction(t *testing.T) {
	cases := []struct {
		action            Action
		hasLeft, hasRight bool
	}{
		{Create, true, false},       // sending a local file up
		{Update, true, true},        // both sides hold it
		{Download, false, true},     // pulling the cloud copy down
		{DeleteRemote, false, true}, // rclone logged the destination object
		{DeleteLocal, true, false},  // the local file is what goes
		{Mkdir, false, false},       // a directory is not a pane row
		{Rmdir, false, false},
		{Touch, true, true},
	}
	for _, c := range cases {
		r := Row{Action: c.action}
		setSides(&r)
		if r.HasLeft != c.hasLeft || r.HasRight != c.hasRight {
			t.Errorf("%s: sides = %v/%v, want %v/%v",
				c.action, r.HasLeft, r.HasRight, c.hasLeft, c.hasRight)
		}
	}
}

// Two collectors must not run at once: the log hook is process-wide and carries no
// job identity, so interleaved collections would produce each other's rows.
func TestBeginRefusesASecondCollector(t *testing.T) {
	first, err := Begin(nil, nil)
	if err != nil {
		t.Fatalf("the first Begin must succeed: %v", err)
	}
	if _, err := Begin(nil, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("the second Begin returned %v, want ErrBusy", err)
	}
	first.End()

	// And the slot is reusable afterwards.
	again, err := Begin(nil, nil)
	if err != nil {
		t.Fatalf("Begin after End must succeed: %v", err)
	}
	again.End()
}
