// Package split plans and performs the directory split that Feishu's per-folder
// entry limit forces.
//
// Feishu allows 1500 direct children in one folder; exceeding it fails with
// 1062507. That is the one dimensional limit a real dataset can hit, so weftsync
// stops earlier (default 1400) and offers to split the directory.
//
// The plan is produced by pure functions over a list of entries, so every scheme
// can be tested without a filesystem. Execution is a separate step, and it is
// deliberately verbose about what it did: it moves the user's files.
package split

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

// Scheme names one way of grouping the files in a directory.
type Scheme string

const (
	// SchemeType groups by file extension, which is what people usually mean by
	// "organise this folder".
	SchemeType Scheme = "type"
	// SchemeInitial groups by the first character of the name.
	SchemeInitial Scheme = "initial"
	// SchemeYear groups by modification year.
	SchemeYear Scheme = "year"
	// SchemeCount splits into equal parts, ignoring any property of the files.
	SchemeCount Scheme = "count"
)

// Schemes lists the supported schemes in the order the UI offers them.
func Schemes() []Scheme {
	return []Scheme{SchemeType, SchemeInitial, SchemeYear, SchemeCount}
}

// ValidScheme reports whether s is one of the supported schemes.
func ValidScheme(s Scheme) bool {
	for _, cand := range Schemes() {
		if cand == s {
			return true
		}
	}
	return false
}

// Entry is one direct child of the directory being split.
type Entry struct {
	// Name is the base name.
	Name string
	// IsDir separates files from subdirectories. Subdirectories are moved whole
	// and are never grouped by extension or year.
	IsDir bool
	Size  int64
	// ModTime is used by SchemeYear; the zero value lands in the "unknown"
	// bucket rather than being guessed into the current year.
	ModTime time.Time
}

// Bucket is one target subdirectory in a plan.
type Bucket struct {
	// Name is the directory to create.
	Name string
	// Files counts the direct children that will live in it.
	Files int
	// Bytes sums their sizes, so the UI can show what is about to move.
	Bytes int64
}

// Move is one file or directory changing place, relative to the pair root.
type Move struct {
	From string
	To   string
	// IsDir carries through from Entry.
	IsDir bool
	Size  int64
	// ModTime is the local modification time at planning time. It is recorded so
	// an undo has something to compare against if the file has changed since.
	ModTime time.Time
}

// Plan is a complete, reviewable description of a split. Nothing here has been
// executed: it is the answer to "what would this scheme do".
type Plan struct {
	// Dir is the directory being split, relative to the pair root. Empty means
	// the root itself.
	Dir string
	// Scheme is what produced this plan.
	Scheme Scheme
	// Buckets is sorted by name.
	Buckets []Bucket
	// Moves is the per-entry mapping, sorted by From.
	Moves []Move
	// Notes carries human-readable caveats, e.g. that a bucket had to be divided
	// again because it was still too large.
	Notes []string
}

// Total counts the entries being moved.
func (p *Plan) Total() int { return len(p.Moves) }

// Bytes sums the sizes being moved.
func (p *Plan) Bytes() int64 {
	var n int64
	for _, m := range p.Moves {
		n += m.Size
	}
	return n
}

// Options tunes plan generation.
type Options struct {
	// MaxPerBucket is the largest number of entries a bucket may hold. Callers
	// that know the backend pass MaxPerBucketFor(its declared limit); the default
	// below is only a fallback for callers that do not.
	MaxPerBucket int
}

// DefaultMaxPerBucket is the fallback bucket size.
//
// It is the value Feishu needs (1500 - Headroom), kept as a default for callers
// that build a plan without asking the backend. The number itself is not Feishu's
// business to own: see limits.go for why the limit lives with the backend.
const DefaultMaxPerBucket = 1400

// BuildPlan groups entries into subdirectories according to scheme.
//
// Every bucket in the result holds at most opts.MaxPerBucket entries. A scheme
// that would produce a larger bucket (2000 .xlsx files, say) has that bucket
// divided again, which is reported in Notes: silently emitting an invalid plan
// would mean the split fails halfway through, after files have already moved.
func BuildPlan(dir string, entries []Entry, scheme Scheme, opts Options) (*Plan, error) {
	if !ValidScheme(scheme) {
		return nil, fmt.Errorf("unknown split scheme %q (expected one of %s)",
			scheme, schemeList())
	}
	if opts.MaxPerBucket <= 0 {
		opts.MaxPerBucket = DefaultMaxPerBucket
	}

	// A stable order makes the plan, the snapshot and this package's tests
	// reproducible. Name order is the only one that is meaningful to a user.
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	groups := groupEntries(sorted, scheme)

	plan := &Plan{Dir: dir, Scheme: scheme}
	for _, g := range groups {
		chunks := chunk(g.entries, opts.MaxPerBucket)
		// The note belongs to the group, not to each chunk. Emitting it inside the
		// loop below printed the same sentence once per resulting directory.
		if len(chunks) > 1 {
			plan.Notes = append(plan.Notes,
				fmt.Sprintf("「%s」有 %d 项，超过每层上限 %d，已再分为 %d 个目录",
					g.name, len(g.entries), opts.MaxPerBucket, len(chunks)))
		}
		for i, chunk := range chunks {
			name := g.name
			if len(chunks) > 1 {
				// Sibling names rather than a nested level: Feishu also limits
				// directory depth to 15, so nesting would spend depth for no
				// benefit.
				name = fmt.Sprintf("%s-%02d", g.name, i+1)
			}
			b := Bucket{Name: name}
			for _, e := range chunk {
				b.Files++
				b.Bytes += e.Size
				plan.Moves = append(plan.Moves, Move{
					From:    relJoin(dir, e.Name),
					To:      relJoin(relJoin(dir, name), e.Name),
					IsDir:   e.IsDir,
					Size:    e.Size,
					ModTime: e.ModTime,
				})
			}
			plan.Buckets = append(plan.Buckets, b)
		}
	}

	sort.SliceStable(plan.Buckets, func(i, j int) bool { return plan.Buckets[i].Name < plan.Buckets[j].Name })
	sort.SliceStable(plan.Moves, func(i, j int) bool { return plan.Moves[i].From < plan.Moves[j].From })

	// A directory name that already exists would collide with a local directory
	// or with an earlier bucket, and the move would overwrite it.
	if err := checkNameCollisions(sorted, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// checkNameCollisions rejects plans whose new directories clash with something
// already present.
func checkNameCollisions(entries []Entry, plan *Plan) error {
	existing := make(map[string]bool, len(entries))
	for _, e := range entries {
		existing[e.Name] = true
	}
	seen := map[string]bool{}
	for _, b := range plan.Buckets {
		if seen[b.Name] {
			return fmt.Errorf("two buckets would be named %q", b.Name)
		}
		seen[b.Name] = true
		// A file and a directory cannot share a name; a directory with that name
		// would be merged into rather than created.
		if existing[b.Name] {
			return fmt.Errorf(
				"cannot create %q: an entry with that name is already in the directory. "+
					"Rename it, or choose a different scheme", b.Name)
		}
	}
	return nil
}

// group is one bucket before chunking.
type group struct {
	name    string
	entries []Entry
}

// groupEntries applies the scheme. Directories are grouped separately from files:
// grouping a folder by its extension is meaningless, and moving a folder into a
// "_png" bucket because it happens to be called "png" would be worse.
func groupEntries(entries []Entry, scheme Scheme) []group {
	order := []string{}
	byName := map[string][]Entry{}
	add := func(name string, e Entry) {
		if _, ok := byName[name]; !ok {
			order = append(order, name)
		}
		byName[name] = append(byName[name], e)
	}

	switch scheme {
	case SchemeType:
		for _, e := range entries {
			if e.IsDir {
				add("_folders", e)
				continue
			}
			add(extBucket(e.Name), e)
		}
	case SchemeInitial:
		for _, e := range entries {
			if e.IsDir {
				add("_folders", e)
				continue
			}
			add(initialBucket(e.Name), e)
		}
	case SchemeYear:
		for _, e := range entries {
			if e.IsDir {
				add("_folders", e)
				continue
			}
			if e.ModTime.IsZero() {
				add("_unknown-date", e)
				continue
			}
			add(e.ModTime.Format("2006"), e)
		}
	case SchemeCount:
		// One group; chunking does the work.
		byName["part"] = entries
		order = []string{"part"}
	}

	sort.Strings(order)
	out := make([]group, 0, len(order))
	for _, name := range order {
		out = append(out, group{name: name, entries: byName[name]})
	}
	return out
}

// extBucket is the directory name for a file's extension.
func extBucket(name string) string {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	if ext == "" {
		return "_noext"
	}
	// Keep the name filesystem-safe and short. Feishu allows most characters, but
	// an extension is attacker-controlled only in the sense that a file may be
	// named ".weird:name"; dropping anything outside a small set avoids a bucket
	// name that cannot be created.
	clean := make([]rune, 0, len(ext))
	for _, r := range ext {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			clean = append(clean, r)
		}
	}
	if len(clean) == 0 {
		return "_noext"
	}
	if len(clean) > 12 {
		clean = clean[:12]
	}
	return "_" + string(clean)
}

// initialBucket is the directory name for a file's first character.
func initialBucket(name string) string {
	if name == "" {
		return "_other"
	}
	r := []rune(strings.ToUpper(name))[0]
	switch {
	case r >= 'A' && r <= 'H':
		return "A-H"
	case r >= 'I' && r <= 'P':
		return "I-P"
	case r >= 'Q' && r <= 'Z':
		return "Q-Z"
	case r >= '0' && r <= '9':
		return "0-9"
	}
	// Non-ASCII names land here. PLAN.md mentions pinyin initials; that needs a
	// pinyin table, and pulling one in for a bucket name is not worth the
	// dependency. The bucket is chunked if it grows too large, so the plan stays
	// valid either way.
	return "_other"
}

// chunk divides entries into slices of at most size.
func chunk(entries []Entry, size int) [][]Entry {
	if size <= 0 || len(entries) <= size {
		return [][]Entry{entries}
	}
	var out [][]Entry
	for start := 0; start < len(entries); start += size {
		end := start + size
		if end > len(entries) {
			end = len(entries)
		}
		out = append(out, entries[start:end])
	}
	return out
}

// relJoin joins path elements, keeping the result relative.
func relJoin(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return path.Join(a, b)
}

func schemeList() string {
	names := make([]string, 0, len(Schemes()))
	for _, s := range Schemes() {
		names = append(names, string(s))
	}
	return strings.Join(names, ", ")
}
