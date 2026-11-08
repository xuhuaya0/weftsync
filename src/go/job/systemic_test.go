package job

import (
	"errors"
	"strings"
	"testing"

	"github.com/rclone/rclone/fs"
)

// fakeBackend satisfies fs.Fs by embedding the interface (left nil, since these
// tests only exercise the optional hooks).
type fakeBackend struct {
	fs.Fs
	gotHook func()
	cause   error
}

func (f *fakeBackend) SetAbortHook(fn func()) { f.gotHook = fn }
func (f *fakeBackend) SystemicFailure() error { return f.cause }

// plainBackend has none of the optional methods: a backend that supports neither
// must be handled without panicking, because rclone can hand us any backend.
type plainBackend struct{ fs.Fs }

// TestInstallAbortHookWiresTheBackend checks that the pass can actually be
// cancelled. Without this the backend latches and stops calling the server, but
// rclone still walks the rest of the tree handing out instant failures.
func TestInstallAbortHookWiresTheBackend(t *testing.T) {
	var called bool
	b := &fakeBackend{}
	installAbortHook(b, func() { called = true })

	if b.gotHook == nil {
		t.Fatal("the abort hook was not installed")
	}
	b.gotHook()
	if !called {
		t.Fatal("the installed hook does not run the caller's function")
	}
}

// TestInstallAbortHookToleratesAPlainBackend guards the fallback path.
func TestInstallAbortHookToleratesAPlainBackend(t *testing.T) {
	installAbortHook(&plainBackend{}, func() { t.Fatal("must not be called") })
	if err := systemicFailureOf(&plainBackend{}); err != nil {
		t.Fatalf("a plain backend must report no systemic failure, got %v", err)
	}
}

// TestSystemicFailureOfReportsTheCause checks the reporting path.
func TestSystemicFailureOfReportsTheCause(t *testing.T) {
	cause := errors.New("feishu files.upload_all: code=1061045 msg=frequency limit")
	if got := systemicFailureOf(&fakeBackend{cause: cause}); got != cause {
		t.Fatalf("systemicFailureOf = %v, want the backend's cause", got)
	}
	if got := systemicFailureOf(&fakeBackend{}); got != nil {
		t.Fatalf("systemicFailureOf = %v, want nil while the backend is healthy", got)
	}
}

// TestSystemicErrorMessageIsActionable pins the wording that an operator reads at
// 3am after a 74,313-file migration stopped on its own.
//
// It has to say three things: how far it got, that this is not a per-file
// problem, and that re-running continues rather than starts over.
func TestSystemicErrorMessageIsActionable(t *testing.T) {
	cause := errors.New("feishu files.upload_all: code=1061045 msg=frequency limit")
	err := error(&SystemicError{Transferred: 9987, Errors: 1200, Cause: cause})

	if !IsSystemic(err) {
		t.Fatal("IsSystemic did not recognise its own type")
	}
	if !errors.Is(err, cause) {
		t.Fatal("Unwrap should expose the backend's cause")
	}
	msg := err.Error()
	for _, want := range []string{"9987", "continue", "saved state"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message should mention %q, got: %s", want, msg)
		}
	}
	if IsSystemic(errors.New("some ordinary failure")) {
		t.Fatal("a non-systemic error must not report as systemic")
	}
}
