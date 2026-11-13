// Package remedy lets an error carry a machine-readable explanation of what went
// wrong and what to run about it.
//
// Why this exists instead of a better sentence: most people meet a failure in the
// console, and the console is Chinese while the backend's errors are English.
// Writing the fix into the message means either shipping Chinese out of a backend
// package or making the UI parse prose, and neither survives the second error
// type. So the error carries the kind and the facts, the backend also supplies the
// exact commands (only it knows its own CLI), and the UI owns the wording - the
// same split that conflict kinds already use.
package remedy

import "errors"

// Command is one runnable fix.
//
// Label is a stable token, not a sentence: the UI maps it to its own wording, so
// the commands can change order or count without the two sides disagreeing about
// what "the second one" is.
type Command struct {
	// Label is one of "inspect" (changes nothing) or "apply" (does the thing).
	Label string `json:"label"`
	Run   string `json:"run"`
}

// Remedy describes one recognised failure.
type Remedy struct {
	// Kind is a stable token; the UI decides what to say about it.
	Kind string `json:"kind"`
	// Fields are the facts the explanation needs - which file, which folder,
	// which tokens. Keys are per kind and documented by the kind's author.
	Fields map[string]string `json:"fields,omitempty"`
	// Commands are the fixes, in the order they should be run. When there is an
	// "inspect" command it comes first and changes nothing.
	Commands []Command `json:"commands,omitempty"`
}

// Carrier is an error that can explain itself.
type Carrier interface {
	Remedy() Remedy
}

// Of returns the remedy carried by err, if there is one.
//
// It walks the whole chain, which matters: a run reports errors.Join of every
// pair, so the interesting error is almost never the outermost one.
func Of(err error) (Remedy, bool) {
	if err == nil {
		return Remedy{}, false
	}
	var c Carrier
	if errors.As(err, &c) {
		r := c.Remedy()
		if r.Kind != "" {
			return r, true
		}
	}
	return Remedy{}, false
}
