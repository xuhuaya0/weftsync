//go:build !windows

package tray

// There is no cross-platform notification area to attach to. On Linux the
// candidates are all unattractive: an X11 tray needs a C library, Wayland has no
// tray protocol at all, and AppIndicator needs both a library and a desktop
// extension. Rather than ship something that works on some desktops and silently
// not on others, this reports the gap and the daemon runs headless - the web UI
// is the interface.

// Tray exists only to keep the API identical across platforms.
type Tray struct{}

// Start reports that there is no tray on this platform.
func Start(cfg Config) (*Tray, error) { return nil, ErrUnsupported }

// Stop does nothing.
func (t *Tray) Stop() {}

// Notify reports that there is no tray to notify through.
func (t *Tray) Notify(title, text string) error { return ErrUnsupported }
