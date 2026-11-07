package autostart

import (
	"path/filepath"
	"strings"
	"testing"
)

// SpecFromInvocation must produce absolute paths.
//
// A login session has no useful working directory, so a relative job file would
// resolve somewhere else or not at all - the entry would look correct and fail
// every morning.
func TestSpecFromInvocationUsesAbsolutePaths(t *testing.T) {
	spec, err := SpecFromInvocation("weftsync", []string{"job.toml"},
		"127.0.0.1:6000", "logs/daemon.log", "conf/rclone.conf", false, "127.0.0.1:5572")
	if err != nil {
		t.Fatalf("SpecFromInvocation: %v", err)
	}

	if !filepath.IsAbs(spec.Exe) {
		t.Errorf("exe is not absolute: %q", spec.Exe)
	}
	for _, a := range spec.Args {
		if strings.HasSuffix(a, ".toml") || strings.HasSuffix(a, ".log") ||
			strings.HasSuffix(a, ".conf") {
			if !filepath.IsAbs(a) {
				t.Errorf("argument %q is not absolute", a)
			}
		}
	}
	if spec.Args[0] != "daemon" {
		t.Errorf("args should start with the subcommand, got %v", spec.Args)
	}
}

// Defaults must not be written out.
//
// Recording the default --addr would freeze it: when the default changes, an old
// entry keeps passing the old value. Only deviations belong in the entry.
func TestSpecFromInvocationOmitsDefaults(t *testing.T) {
	spec, err := SpecFromInvocation("weftsync", []string{"job.toml"},
		"127.0.0.1:5572", "", "", false, "127.0.0.1:5572")
	if err != nil {
		t.Fatalf("SpecFromInvocation: %v", err)
	}
	joined := strings.Join(spec.Args, " ")
	for _, unwanted := range []string{"--addr", "--log-file", "--config", "--run-now"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("%s should not be recorded when it holds its default: %v",
				unwanted, spec.Args)
		}
	}

	// A non-default value must be recorded, with its absolute form.
	spec, err = SpecFromInvocation("weftsync", []string{"job.toml"},
		"127.0.0.1:6000", "x.log", "", false, "127.0.0.1:5572")
	if err != nil {
		t.Fatalf("SpecFromInvocation: %v", err)
	}
	joined = strings.Join(spec.Args, " ")
	if !strings.Contains(joined, "--addr 127.0.0.1:6000") {
		t.Errorf("a non-default address must be recorded: %v", spec.Args)
	}
	if !strings.Contains(joined, "--log-file") {
		t.Errorf("a log file must be recorded: %v", spec.Args)
	}
}

func TestQualifiedLeavesEmptyAlone(t *testing.T) {
	got, err := Qualified("   ")
	if err != nil {
		t.Fatalf("Qualified: %v", err)
	}
	if got != "" {
		t.Errorf("blank input should stay empty, got %q", got)
	}
}
