package job

import "testing"

// A one-way sync treats "the cloud copy the snapshot recorded is gone" as an
// upload, not as a conflict.
//
// Measured on a real Feishu pair (2026-11-17): three files uploaded, one deleted
// from the cloud by hand, second run refused the whole pair with a conflict the
// user had to resolve by hand. Their question was why a one-way update needs to
// report a conflict at all when the local side is the source of truth - and the
// answer is that it should not. After the change the same pair reports "1 cloud
// file(s) ... will be uploaded again" and transfers exactly that one file.
func TestSplitRestorableTreatsADeletedCloudCopyAsAnUpload(t *testing.T) {
	in := []Conflict{
		{Path: "b.txt", Kind: ConflictKindRemoteDeleted},
		{Path: "d.txt", Kind: ConflictKindRemoteDeleted},
	}
	restorable, blocking := splitRestorable(in)
	if len(blocking) != 0 {
		t.Errorf("%d conflict(s) still block a one-way sync: %v", len(blocking), blocking)
	}
	if len(restorable) != 2 {
		t.Fatalf("restorable = %d, want 2", len(restorable))
	}
	if restorable[0].Path != "b.txt" || restorable[1].Path != "d.txt" {
		t.Errorf("order changed: %v", restorable)
	}
}

// The other two kinds must keep blocking even when they arrive next to a
// restorable one: overwriting somebody else's file, or adding a third copy of a
// name that already has several, destroys data that a run cannot put back.
func TestSplitRestorableKeepsDataLosingKindsBlocking(t *testing.T) {
	in := []Conflict{
		{Path: "gone.txt", Kind: ConflictKindRemoteDeleted},
		{Path: "theirs.txt", Kind: ConflictKindRemoteReplaced},
		{Path: "dupe.txt", Kind: ConflictKindDuplicateName},
	}
	restorable, blocking := splitRestorable(in)
	if len(restorable) != 1 || restorable[0].Path != "gone.txt" {
		t.Fatalf("restorable = %v, want only gone.txt", restorable)
	}
	if len(blocking) != 2 {
		t.Fatalf("blocking = %d, want 2", len(blocking))
	}
	if blocking[0].Path != "theirs.txt" || blocking[1].Path != "dupe.txt" {
		t.Errorf("blocking = %v", blocking)
	}
}

// Nothing to classify must stay silent, so a pair with no conflicts is not
// reported as "0 cloud files … gone".
func TestSplitRestorableOfNothing(t *testing.T) {
	restorable, blocking := splitRestorable(nil)
	if len(restorable) != 0 || len(blocking) != 0 {
		t.Errorf("restorable=%v blocking=%v, want empty", restorable, blocking)
	}
}
