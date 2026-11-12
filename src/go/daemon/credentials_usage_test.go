package daemon

import (
	"io"
	"path/filepath"
	"testing"

	"weftsync/job"
)

// "Which jobs use this connection" is a claim the page makes every time a card
// renders, and the user asked why it said "nobody uses this" for a connection they
// believed was in use. The answer was that their pair's cloud path is
// ":local:<path>" - a local folder standing in for the cloud side - so the named
// remote really is not referenced.
//
// That is easy to get wrong in either direction: counting ":local:..." as a
// reference to a remote called ":local", or missing "test:path". These cases pin
// the rule.
func TestPairRemoteName(t *testing.T) {
	cases := []struct {
		name   string
		pair   job.Pair
		want   string
		reason string
	}{
		{
			name: "named remote with a path",
			pair: job.Pair{Remote: job.Endpoint{Root: "test:team/docs"}},
			want: "test",
		},
		{
			name: "named remote, no path",
			pair: job.Pair{Remote: job.Endpoint{Root: "test:"}},
			want: "test",
		},
		{
			// The rclone inline backend prefix. There is no remote called
			// ":local", and a colon at index 0 must not be read as a separator.
			name:   "inline local backend is not a remote",
			pair:   job.Pair{Remote: job.Endpoint{Root: ":local:C:/data/dst"}},
			want:   "",
			reason: "this is the case that made the panel look wrong",
		},
		{
			name: "backend field wins over the root",
			pair: job.Pair{Remote: job.Endpoint{Backend: "edge", Root: "anything:here"}},
			want: "edge",
		},
		{
			name: "backend field alone",
			pair: job.Pair{Remote: job.Endpoint{Backend: "edge"}},
			want: "edge",
		},
		{
			name:   "plain local path",
			pair:   job.Pair{Remote: job.Endpoint{Root: "C:/data/dst"}},
			want:   "",
			reason: "a Windows drive letter is not a remote either",
		},
		{
			name: "empty",
			pair: job.Pair{},
			want: "",
		},
		{
			name: "surrounding space is trimmed",
			pair: job.Pair{Remote: job.Endpoint{Root: "  test:docs  "}},
			want: "test",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := c.pair
			got := pairRemoteName(&p)
			if got != c.want {
				t.Errorf("pairRemoteName(%q backend=%q) = %q, want %q (%s)",
					p.Remote.Root, p.Remote.Backend, got, c.want, c.reason)
			}
		})
	}
}

// usedRemotes must report the same names pairRemoteName produces, deduplicated
// across pairs and sorted, and must not invent an entry for ":local:".
func TestUsedRemotesOnlyCountsNamedRemotes(t *testing.T) {
	dir := t.TempDir()
	slash := func(s string) string { return filepath.ToSlash(s) }
	body := `
[job]
name = "a"

[[pair]]
name = "p1"
[pair.local]
root = "` + slash(dir) + `/src1"
[pair.remote]
root = "test:team/docs"

[[pair]]
name = "p2"
[pair.local]
root = "` + slash(dir) + `/src2"
[pair.remote]
root = "test:other"

[[pair]]
name = "p3"
[pair.local]
root = "` + slash(dir) + `/src3"
[pair.remote]
root = ":local:` + slash(dir) + `/dst"
`
	d, err := New([]string{writeJobFile(t, dir, "a", body)}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	used := d.usedRemotes()
	if got := used["test"]; len(got) != 1 || got[0] != "a" {
		t.Errorf("used[test] = %v, want [a] once even though two pairs share it", got)
	}
	if _, ok := used[":local"]; ok {
		t.Error(`used[":local"] exists; a colon at index 0 is not a remote separator`)
	}
	if len(used) != 1 {
		t.Errorf("used = %v, want only the test remote", used)
	}
}
