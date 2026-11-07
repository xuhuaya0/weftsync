//go:build windows

package tray

import (
	"sync/atomic"
	"testing"
	"time"
)

// Starting the tray must produce a real window and a live icon, and stopping it
// must not hang. This touches the real notification area, so it only runs when a
// desktop session is available; in a service session Shell_NotifyIcon legitimately
// fails and the daemon must survive that (which is a different test).
func TestStartStop(t *testing.T) {
	var ranAll, quit int32
	tr, err := Start(Config{
		Tooltip:    "weftsync test",
		ConsoleURL: "http://127.0.0.1:1/",
		OnRunAll:   func() { atomic.AddInt32(&ranAll, 1) },
		OnQuit:     func() { atomic.AddInt32(&quit, 1) },
	})
	if err != nil {
		t.Skipf("no notification area in this session: %v", err)
	}

	tr.mu.Lock()
	hwnd := tr.hwnd
	hIcon := tr.hIcon
	tr.mu.Unlock()

	if hwnd == 0 {
		t.Fatal("Start returned without a window")
	}
	if hIcon == 0 {
		t.Fatal("Start returned without an icon")
	}

	isWindow := user32.NewProc("IsWindow")
	if ok, _, _ := isWindow.Call(hwnd); ok == 0 {
		t.Error("the tray window is not a live window")
	}

	// A balloon is the notification path the daemon uses for guard trips.
	if err := tr.Notify("weftsync", "test notification"); err != nil {
		t.Errorf("Notify: %v", err)
	}

	// Stopping must complete; a hang here would block daemon shutdown.
	done := make(chan struct{})
	go func() {
		tr.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return; daemon shutdown would hang")
	}

	if ok, _, _ := isWindow.Call(hwnd); ok != 0 {
		t.Error("the window still exists after Stop")
	}

	// Stop is documented as safe to call twice; shutdown paths tend to.
	tr.Stop()

	if atomic.LoadInt32(&ranAll) != 0 || atomic.LoadInt32(&quit) != 0 {
		t.Error("menu callbacks must not fire on their own")
	}
}

// Two trays at once must not collide on the window class.
func TestTwoTraysCoexist(t *testing.T) {
	a, errA := Start(Config{Tooltip: "a"})
	if errA != nil {
		t.Skipf("no notification area in this session: %v", errA)
	}
	defer a.Stop()

	b, errB := Start(Config{Tooltip: "b"})
	if errB != nil {
		t.Fatalf("a second tray should work, got: %v", errB)
	}
	defer b.Stop()

	a.mu.Lock()
	bh := b.hwnd
	a.mu.Unlock()
	if bh == 0 {
		t.Error("the second tray has no window")
	}
}

// A nil callback must be tolerated: the tray is optional and its config is
// assembled at the call site.
func TestNilCallbacks(t *testing.T) {
	tr, err := Start(Config{Tooltip: "x"})
	if err != nil {
		t.Skipf("no notification area in this session: %v", err)
	}
	defer tr.Stop()

	// Drive the command handler directly instead of clicking.
	tr.wndProc(tr.hwnd, wmCommand, menuRunAll, 0)
	tr.wndProc(tr.hwnd, wmCommand, menuQuit, 0)
}
