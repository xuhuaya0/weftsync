package job

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// phaseLog records what a run reported, in order.
//
// The callback is called from the run goroutine while nothing else touches the
// slice, but the mutex costs nothing and keeps the helper usable from a test that
// later decides to read concurrently.
type phaseLog struct {
	mu     sync.Mutex
	phases []Phase
}

func (l *phaseLog) on(p Phase) {
	l.mu.Lock()
	l.phases = append(l.phases, p)
	l.mu.Unlock()
}

func (l *phaseLog) all() []Phase {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Phase(nil), l.phases...)
}

func (l *phaseLog) kinds() map[string]bool {
	out := map[string]bool{}
	for _, p := range l.all() {
		out[p.Kind] = true
	}
	return out
}

// A preview must name what it is doing.
//
// Why this is a test and not a comment: the whole point of the phase reporting is
// that a long scan and a hung process are otherwise indistinguishable, and that
// failure mode is invisible to any assertion about the result. Measured before
// this existed: a 265-second comparison displayed only a fixed sentence.
func TestPlanReportsItsPhases(t *testing.T) {
	cfg, local, _ := localPair(t, VariantUpdate, "")
	makeFiles(t, local, map[string]string{
		"a.txt": "one",
		"b.txt": "two",
	})

	log := &phaseLog{}
	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, Options{
		DryRun:      true,
		CollectPlan: true,
		OnPhase:     log.on,
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}

	got := log.kinds()
	for _, want := range []string{PhaseOpen, PhaseScan} {
		if !got[want] {
			t.Errorf("phase %q was never reported; got %v", want, got)
		}
	}

	// A write phase in a preview would be a lie: nothing is written. The update
	// variant runs the same code for both modes, so this is the regression that
	// matters.
	if got[PhaseWrite] {
		t.Error("a preview reported PhaseWrite, but a preview writes nothing")
	}

	// Every phase names its pair: with two pairs, "scanning" alone does not say
	// which folder is taking the time.
	for _, p := range log.all() {
		if p.Pair == "" {
			t.Errorf("phase %q has no pair name", p.Kind)
		}
	}

	// The plan still worked; phases are reporting, not a change in behaviour.
	if len(res.Plan) == 0 {
		t.Error("the preview produced no plan rows")
	}
}

// A real run reports the write phase, and the counters that come with it.
func TestRunReportsWritePhase(t *testing.T) {
	cfg, local, remote := localPair(t, VariantUpdate, "")
	makeFiles(t, local, map[string]string{"a.txt": "one"})

	log := &phaseLog{}
	if _, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{OnPhase: log.on}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !log.kinds()[PhaseWrite] {
		t.Errorf("a real run never reported PhaseWrite; got %v", log.kinds())
	}
	if _, err := os.Stat(filepath.Join(remote, "a.txt")); err != nil {
		t.Errorf("the file did not arrive: %v", err)
	}
}

// The existence check that turns "create" into "update" must report a total, or
// the console can only show an open-ended spinner for a phase that makes one
// network call per file.
func TestVerifyPhaseCountsItsWork(t *testing.T) {
	cfg, local, remote := localPair(t, VariantUpdate, "")
	makeFiles(t, local, map[string]string{
		"a.txt": "one",
		"b.txt": "two",
		"c.txt": "three",
	})
	// Pre-existing destination copies are what make the check do anything: with
	// an empty destination every row stays "create" only by failing the check.
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		writeFile(t, filepath.Join(remote, name), "old")
	}

	log := &phaseLog{}
	if _, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, Options{
		DryRun:      true,
		CollectPlan: true,
		OnPhase:     log.on,
	}); err != nil {
		t.Fatalf("preview: %v", err)
	}

	var verify []Phase
	for _, p := range log.all() {
		if p.Kind == PhaseVerify {
			verify = append(verify, p)
		}
	}
	if len(verify) == 0 {
		t.Fatalf("no %q phase reported; got %v", PhaseVerify, log.kinds())
	}
	last := verify[len(verify)-1]
	if last.Total == 0 {
		t.Fatalf("verify phase never reported a total: %+v", last)
	}
	if last.Done != last.Total {
		t.Errorf("verify ended at %d of %d, so the last report is not the finish line",
			last.Done, last.Total)
	}
}
