// Command weftsync is a custom rclone build that adds the Feishu (Lark) Drive
// backend.
//
// Rather than forking rclone, we depend on it as a library and only supply:
//   - our own backend package
//   - this thin main() that wires it into rclone's command layer
//
// That keeps us able to follow upstream without rebasing a fork.
package main

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	_ "github.com/rclone/rclone/backend/all" // all upstream backends
	"github.com/rclone/rclone/cmd"
	_ "github.com/rclone/rclone/cmd/all" // all upstream commands
	_ "github.com/rclone/rclone/lib/plugin"

	_ "weftsync/backend/feishu"     // our backend
	"weftsync/browser"              // open the console in a browser
	daemoncmd "weftsync/cmd/daemon" // `daemon`: scheduled runs + local API
	_ "weftsync/cmd/run"            // `run`: guarded sync
)

func main() {
	// No arguments means someone double-clicked the executable.
	//
	// 2026-11-18, the user: 「为啥我双击exe经常闪退呢」, 「如果不能双击打开这个exe有啥用」,
	// then 「日志不是有记录地方吗，为啥要一个窗口，我在gui的界面中不是可以看到吗」.
	// All three were right. Double-clicking used to run the CLI with no command:
	// usage text, exit 2, window gone. For a program whose interface is a web page,
	// the useful answer to "open this" is the console, with no console window
	// hanging around afterwards - the 日志 page already shows the same lines.
	//
	// Two cases, because "double-click" is also how people reopen a program that is
	// already running:
	//
	//   already answering   open a browser at it and leave it alone. Starting a
	//                       second daemon would fail on the port, which is the
	//                       silent-flash problem again.
	//   not answering       start the daemon, hide the window once it serves, and
	//                       open the browser.
	//
	// The rewrite goes through os.Args so the daemon command parses exactly the
	// flags it would from a shell; nothing here reimplements the CLI.
	if len(os.Args) == 1 {
		consoleURL := "http://" + daemoncmd.DefaultAddr + "/"
		if reachable(consoleURL) {
			_ = browser.Open(consoleURL)
			return
		}
		args := []string{"daemon", "--open", "--hide-console"}
		if log := defaultLogFile(); log != "" {
			args = append(args, "--log-file", log)
		}
		os.Args = append(os.Args, args...)
	}
	cmd.Main()
}

// reachable asks the daemon already listening on the default address whether it
// is alive.
//
// Short timeout on purpose: this runs before anything is on screen, and a
// double-click that pauses for a second reads as "it hangs". A refused connection
// answers instantly; the timeout only covers a machine that is dropping packets.
func reachable(consoleURL string) bool {
	c := &http.Client{Timeout: 400 * time.Millisecond}
	resp, err := c.Get(consoleURL + "api/health")
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// defaultLogFile is where a double-clicked daemon writes its log.
//
// Without a console, stdout goes nowhere: the 日志 page keeps recent lines in
// memory but loses everything on restart, and a crash needs a record that
// outlives the process. The per-user config directory is writable without
// elevation, which a directory next to the exe is not (Program Files).
//
// Empty means "could not work it out", and the caller simply omits the flag: a
// missing log file must not stop the sync.
func defaultLogFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	dir = filepath.Join(dir, "weftsync")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	return filepath.Join(dir, "daemon.log")
}
