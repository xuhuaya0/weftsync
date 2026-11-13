package remedy

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// mustJSON renders v as JSON, failing the test if it cannot.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// fakeError is a carrier with a preset remedy.
type fakeError struct {
	r    Remedy
	text string
}

func (e *fakeError) Error() string  { return e.text }
func (e *fakeError) Remedy() Remedy { return e.r }

// notACarrier is an ordinary error.
var errPlain = errors.New("plain")

// Of must find the carrier wherever it sits.
//
// This is the whole point of the package: a run reports errors.Join of every
// pair, so the error that can explain itself is rarely the outermost one.
func TestOfFindsCarrierThroughWrappingAndJoin(t *testing.T) {
	carrier := &fakeError{
		r:    Remedy{Kind: "k", Fields: map[string]string{"a": "b"}},
		text: "specific",
	}

	chains := map[string]error{
		"direct":         carrier,
		"fmt.Errorf(%w)": fmt.Errorf("wrap: %w", carrier),
		"two wraps":      fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", carrier)),
		"join":           errors.Join(errPlain, carrier),
		"join nested":    errors.Join(fmt.Errorf("p1: %w", errPlain), fmt.Errorf("p2: %w", carrier)),
	}

	for name, err := range chains {
		r, ok := Of(err)
		if !ok {
			t.Errorf("%s: remedy not found", name)
			continue
		}
		if r.Kind != "k" || r.Fields["a"] != "b" {
			t.Errorf("%s: got %+v", name, r)
		}
	}
}

// An ordinary error carries no remedy, and neither does nil.
func TestOfIgnoresPlainErrors(t *testing.T) {
	for name, err := range map[string]error{
		"nil":        nil,
		"plain":      errPlain,
		"wrapped":    fmt.Errorf("wrap: %w", errPlain),
		"joined":     errors.Join(errPlain, errPlain),
		"empty kind": &fakeError{text: "no kind"},
	} {
		if r, ok := Of(err); ok {
			t.Errorf("%s: unexpected remedy %+v", name, r)
		}
	}
}

// JSON is what crosses to the console, so the field names are part of the
// contract and must not drift silently.
func TestRemedyJSONShape(t *testing.T) {
	got := mustJSON(t, Remedy{
		Kind:   "k",
		Fields: map[string]string{"path": "a/b"},
		Commands: []Command{
			{Label: "inspect", Run: "cmd one"},
			{Label: "apply", Run: "cmd two"},
		},
	})
	want := `{"kind":"k","fields":{"path":"a/b"},"commands":[{"label":"inspect","run":"cmd one"},{"label":"apply","run":"cmd two"}]}`
	if got != want {
		t.Errorf("json changed:\n got %s\nwant %s", got, want)
	}
}
