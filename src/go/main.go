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
	"os"

	_ "github.com/rclone/rclone/backend/all" // all upstream backends
	"github.com/rclone/rclone/cmd"
	_ "github.com/rclone/rclone/cmd/all" // all upstream commands
	_ "github.com/rclone/rclone/lib/plugin"

	_ "weftsync/backend/feishu" // our backend
	_ "weftsync/cmd/daemon"     // `daemon`: scheduled runs + local API
	_ "weftsync/cmd/run"        // `run`: guarded sync
)

func main() {
	// No arguments means someone double-clicked the executable.
	//
	// 2026-11-18, the user: 「为啥我双击exe经常闪退呢」, then 「如果不能双击打开这个
	// exe有啥用」. Both were right. Double-clicking ran the CLI with no command,
	// which printed the usage text and exited with status 2 - the console window
	// closed before anybody could read it, which is what "闪退" looked like.
	//
	// For a program whose whole interface is a web page, the useful answer to
	// "open this" is: run the daemon and open the console. Zero job files is
	// allowed (see cmd/daemon), so a brand-new install lands on 「还没有配置」 and
	// can create everything from there.
	//
	// The rewrite happens before cmd.Main() so the daemon command parses exactly
	// the same flags it would from a shell; nothing here reimplements the CLI.
	if len(os.Args) == 1 {
		os.Args = append(os.Args, "daemon", "--open")
	}
	cmd.Main()
}
