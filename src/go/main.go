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
	_ "github.com/rclone/rclone/backend/all" // all upstream backends
	"github.com/rclone/rclone/cmd"
	_ "github.com/rclone/rclone/cmd/all" // all upstream commands
	_ "github.com/rclone/rclone/lib/plugin"

	_ "weftsync/backend/feishu" // our backend
	_ "weftsync/cmd/run"        // the `run` command (guarded sync)
)

func main() {
	cmd.Main()
}
