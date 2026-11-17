package job

import (
	"errors"
	"strings"
	"testing"

	"weftsync/remedy"
)

// A conflict abort must reach the console as data, not only as prose.
//
// Measured on a real run: the user deleted one file in Feishu, pressed 同步, and got
// the same 400-character English paragraph in three places (the failure block, the
// result card and the status bar) with nowhere saying WHICH file. The conflict list
// was in this very error's pair result, but the error carried only a count.
func TestConflictErrorCarriesItsPaths(t *testing.T) {
	e := &ConflictError{
		Pair:  "pair-2",
		Count: 2,
		Paths: []string{"a/b.xlsx", "c.xlsx"},
		Kinds: map[string]string{"a/b.xlsx": ConflictKindRemoteDeleted, "c.xlsx": ConflictKindDuplicateName},
	}

	r, ok := remedy.Of(errors.Join(errors.New("pair 1: x"), e))
	if !ok {
		t.Fatal("remedy not found through the joined error, which is how a run reports")
	}
	if r.Kind != "conflict" {
		t.Errorf("kind = %q, want conflict", r.Kind)
	}
	if got := r.Fields["pair"]; got != "pair-2" {
		t.Errorf("pair = %q", got)
	}
	if got := r.Fields["count"]; got != "2" {
		t.Errorf("count = %q", got)
	}
	// Paths travel newline-separated so one field can hold the list; kinds travel as
	// "path\tkind" so the two stay aligned without a second structure.
	if got := r.Fields["paths"]; got != "a/b.xlsx\nc.xlsx" {
		t.Errorf("paths = %q", got)
	}
	kinds := r.Fields["kinds"]
	if !strings.Contains(kinds, "a/b.xlsx\t"+ConflictKindRemoteDeleted) {
		t.Errorf("kinds missing the deleted path: %q", kinds)
	}
	if !strings.Contains(kinds, "c.xlsx\t"+ConflictKindDuplicateName) {
		t.Errorf("kinds missing the duplicate path: %q", kinds)
	}
	// No commands: every action is a button on the conflicts page. A command here
	// would send the user to a terminal for something one click away.
	if len(r.Commands) != 0 {
		t.Errorf("conflict remedy offers commands: %+v", r.Commands)
	}
}

// A conflict with a count but no paths must not invent one: the console falls back
// to the count, and a fabricated file name would be worse than no name.
func TestConflictErrorWithoutPathsHasNoInventedName(t *testing.T) {
	e := &ConflictError{Pair: "p", Count: 3}
	r, ok := remedy.Of(e)
	if !ok {
		t.Fatal("remedy missing")
	}
	if got := r.Fields["paths"]; got != "" {
		t.Errorf("paths = %q, want empty", got)
	}
	if got := r.Fields["count"]; got != "3" {
		t.Errorf("count = %q", got)
	}
	if _, has := r.Fields["kinds"]; has {
		t.Error("kinds present without paths")
	}
}

// keep-both is not a failure of the transfer, and the console must be able to say
// so instead of printing "nothing was uploaded".
func TestConflictErrorKeptBothIsMarked(t *testing.T) {
	e := &ConflictError{Pair: "p", Count: 1, Paths: []string{"a.txt"},
		Kinds: map[string]string{"a.txt": ConflictKindRemoteReplaced}, KeptBoth: true}
	r, _ := remedy.Of(e)
	if r.Fields["kept_both"] != "true" {
		t.Errorf("kept_both = %q, want true", r.Fields["kept_both"])
	}
}

// The English sentence stays for logs and for a caller that only has Error(). It is
// no longer what the console shows, but it must not lose the facts either.
func TestConflictErrorTextStillNamesThePair(t *testing.T) {
	e := &ConflictError{Pair: "pair-2", Count: 1}
	if !strings.Contains(e.Error(), "pair-2") || !strings.Contains(e.Error(), "NOTHING") {
		t.Errorf("error text lost its content: %s", e.Error())
	}
}
