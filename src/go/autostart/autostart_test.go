package autostart

import (
	"path/filepath"
	"strings"
	"testing"
)

// SpecFrom must produce absolute paths.
//
// A login session has no useful working directory, so a relative job file would
// resolve somewhere else or not at all - the entry would look correct and fail
// every morning.
func TestSpecFromUsesAbsolutePaths(t *testing.T) {
	spec, err := SpecFrom(Invocation{
		Exe: "weftsync", JobFiles: []string{"job.toml"},
		Addr: "127.0.0.1:6000", LogFile: "logs/daemon.log",
		ConfigFile: "conf/rclone.conf", DefaultAddr: "127.0.0.1:5572",
	})
	if err != nil {
		t.Fatalf("SpecFrom: %v", err)
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
func TestSpecFromOmitsDefaults(t *testing.T) {
	spec, err := SpecFrom(Invocation{
		Exe: "weftsync", JobFiles: []string{"job.toml"},
		Addr: "127.0.0.1:5572", DefaultAddr: "127.0.0.1:5572",
	})
	if err != nil {
		t.Fatalf("SpecFrom: %v", err)
	}
	joined := strings.Join(spec.Args, " ")
	for _, unwanted := range []string{"--addr", "--log-file", "--config", "--run-now", "--tray", "--no-tray"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("%s should not be recorded when it holds its default: %v",
				unwanted, spec.Args)
		}
	}

	// A non-default value must be recorded, with its absolute form.
	spec, err = SpecFrom(Invocation{
		Exe: "weftsync", JobFiles: []string{"job.toml"},
		Addr: "127.0.0.1:6000", LogFile: "x.log", DefaultAddr: "127.0.0.1:5572",
	})
	if err != nil {
		t.Fatalf("SpecFrom: %v", err)
	}
	joined = strings.Join(spec.Args, " ")
	if !strings.Contains(joined, "--addr 127.0.0.1:6000") {
		t.Errorf("a non-default address must be recorded: %v", spec.Args)
	}
	if !strings.Contains(joined, "--log-file") {
		t.Errorf("a log file must be recorded: %v", spec.Args)
	}
}

// Turning the tray OFF has to be carried into the entry.
//
// The icon is on by default since 2026-11-18, so an entry that says nothing gets
// the icon back at the next login - the opposite of what someone who passed
// --no-tray asked for. The old version of this test asserted the mirror image
// (that --tray was recorded); it was correct while the tray was opt-in.
func TestSpecFromKeepsTrayOffFlag(t *testing.T) {
	spec, err := SpecFrom(Invocation{
		Exe: "weftsync", JobFiles: []string{"job.toml"}, NoTray: true,
		DefaultAddr: "127.0.0.1:5572",
	})
	if err != nil {
		t.Fatalf("SpecFrom: %v", err)
	}
	joined := strings.Join(spec.Args, " ")
	if !strings.Contains(joined, "--no-tray") {
		t.Errorf("--no-tray must be recorded when the tray is off: %v", spec.Args)
	}
	if strings.Contains(joined, " --tray") {
		t.Errorf("--tray must not be written when it is already the default: %v", spec.Args)
	}
}

// The default (icon on) leaves the entry silent.
//
// Recording a default freezes it: a later change to the default would never
// reach an entry that spelled the old value out. The zero value of NoTray is
// exactly this case, which is why the field is named for the negative - a zero
// Invocation must produce the default behaviour, not the opposite.
func TestSpecFromOmitsTrayWhenOn(t *testing.T) {
	spec, err := SpecFrom(Invocation{
		Exe: "weftsync", JobFiles: []string{"job.toml"},
		DefaultAddr: "127.0.0.1:5572",
	})
	if err != nil {
		t.Fatalf("SpecFrom: %v", err)
	}
	joined := strings.Join(spec.Args, " ")
	if strings.Contains(joined, "tray") {
		t.Errorf("the tray default must not be written into the entry: %v", spec.Args)
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
