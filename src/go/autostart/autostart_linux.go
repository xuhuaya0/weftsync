//go:build linux

package autostart

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Linux uses a desktop entry in the per-user autostart directory. Every desktop
// environment worth naming reads it, and it needs no root.
const fileName = "weftsync.desktop"

// desktopReserved are the characters the Desktop Entry spec wants quoted inside
// an Exec value.
const desktopReserved = " \t\n\"'\\><~|&;$*?#()`"

// Supported reports whether autostart can be managed here.
func Supported() bool { return true }

// entryPath is ~/.config/autostart/weftsync.desktop.
func entryPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate the config directory: %w", err)
	}
	return filepath.Join(dir, "autostart", fileName), nil
}

// Location is where the entry lives, in a form a user can look up.
func Location() string {
	p, err := entryPath()
	if err != nil {
		return "~/.config/autostart/" + fileName
	}
	return p
}

// Command renders the Exec value for a spec.
//
// Two escaping layers apply and they nest: the value is unescaped as a whole
// first (so a literal backslash must be written as "\\"), and only then is the
// quoting rule applied (so inside quotes " ` and $ need a backslash of their
// own). Doing only one of the two is the usual reason a path with a space ends
// up as several arguments.
func Command(spec Spec) string {
	parts := make([]string, 0, len(spec.Args)+1)
	parts = append(parts, execQuote(spec.Exe))
	for _, a := range spec.Args {
		parts = append(parts, execQuote(a))
	}
	return strings.Join(parts, " ")
}

func execQuote(arg string) string {
	if arg == "" {
		return `""`
	}
	value := strings.ReplaceAll(arg, `\`, `\\`)
	value = strings.ReplaceAll(value, "%", "%%")

	if !strings.ContainsAny(arg, desktopReserved) {
		return value
	}
	for _, r := range []string{`"`, "`", "$"} {
		value = strings.ReplaceAll(value, r, `\`+r)
	}
	return `"` + value + `"`
}

// parseExec splits an Exec value back into arguments. It inverts execQuote.
func parseExec(value string) []string {
	var (
		args []string
		cur  strings.Builder
		got  bool
		inQ  bool
	)
	end := func() {
		if got {
			args = append(args, strings.ReplaceAll(cur.String(), "%%", "%"))
			cur.Reset()
			got = false
		}
	}
	rs := []rune(value)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\\' && i+1 < len(rs):
			switch rs[i+1] {
			case '\\':
				cur.WriteRune('\\')
				i++
			case '"', '`', '$':
				if inQ {
					cur.WriteRune(rs[i+1])
					i++
				} else {
					cur.WriteRune('\\')
				}
			default:
				cur.WriteRune('\\')
			}
			got = true
		case r == '"':
			inQ = !inQ
			got = true
		case (r == ' ' || r == '\t') && !inQ:
			end()
		default:
			cur.WriteRune(r)
			got = true
		}
	}
	end()
	return args
}

// readExec returns the Exec value from the entry, or "" when there is none.
func readExec(path string) (string, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read %s: %w", path, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Exec=") {
			return strings.TrimPrefix(line, "Exec="), true, nil
		}
	}
	return "", true, nil
}

// Read inspects the current entry and compares it with want.
func Read(want Spec) (Status, error) {
	path, err := entryPath()
	if err != nil {
		return Status{}, err
	}
	st := Status{Supported: true, Location: path}

	value, exists, err := readExec(path)
	if err != nil || !exists {
		return st, err
	}
	st.Enabled = true
	st.Command = value
	if want.Exe != "" {
		got := parseExec(value)
		st.Matches = len(got) == len(want.Args)+1 &&
			got[0] == want.Exe && equal(got[1:], want.Args)
	}
	return st, nil
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Enable writes the entry, replacing any previous one.
func Enable(spec Spec) error {
	if strings.TrimSpace(spec.Exe) == "" {
		return errors.New("autostart: the executable path is empty")
	}
	path, err := entryPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create the autostart directory: %w", err)
	}

	body := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=weftsync\n" +
		"Comment=Sync local folders to cloud storage\n" +
		"Exec=" + Command(spec) + "\n" +
		"Terminal=false\n" +
		"X-GNOME-Autostart-enabled=true\n"

	// Write then rename so a half-written entry cannot be left behind for the
	// login session to choke on.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install %s: %w", path, err)
	}
	return nil
}

// Disable removes the entry. Removing something that is not there is not an
// error: the caller asked for "not enabled", and that is the state afterwards.
func Disable() error {
	path, err := entryPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}
