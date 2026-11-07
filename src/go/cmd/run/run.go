// Package run provides the `run` command: execute a job file with the delete
// guard and versioning applied.
//
// It sits above rclone's own `sync` command on purpose. rclone's --max-delete
// deletes up to the limit and only then aborts (measured: --max-delete 1 against
// two pending deletions removed one file), which is exactly the behaviour a
// permanent cloud delete cannot afford. This command measures first and only
// then decides.
package run

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/rclone/rclone/cmd"
	"github.com/spf13/cobra"

	"weftsync/job"
)

func init() {
	cmd.Root.AddCommand(commandDefinition)
	flags := commandDefinition.Flags()
	flags.BoolVar(&dryRun, "plan", false,
		"Show what each pair would do without changing anything")
}

var dryRun bool

var commandDefinition = &cobra.Command{
	Use:   "run job.toml",
	Short: `Run a weftsync job file (guarded sync with delete protection)`,
	Long: `Run every pair listed in a job file.

Each pair maps a local directory onto a remote directory using one of the
FreeFileSync variants:

  variant = "update"   add and overwrite only, never delete (the default)
  variant = "mirror"   make the destination identical, deleting extras

For "mirror", a pre-flight pass measures how much would be deleted. If that
exceeds max_delete the whole run is abandoned and NOTHING is deleted, which is
stricter than rclone's own --max-delete (that one deletes up to the limit and
then aborts). The destination this project targets has no trash, so a partial
delete is not recoverable.

deletion = "permanent" (default) removes retired files.
deletion = "versioning" moves them into versioning_dir instead, using a
server-side move, so no data is transferred.

Example job file:

|||toml
[job]
name = "nightly-backup"
variant = "mirror"
[job.sync]
max_delete = 50
deletion = "versioning"
versioning_dir = "_weftsync_trash"
versioning_naming = "timestamp-folder"

[[pair]]
name = "work-docs"
[pair.local]
root = "D:/work/docs"
[pair.remote]
backend = "feishu"
root = "team/docs"
|||
`,
	Run: func(command *cobra.Command, args []string) {
		cmd.CheckArgs(1, 1, command, args)

		cmd.Run(false, false, command, func() error {
			ctx := context.Background()

			cfg, err := job.Load(args[0])
			if err != nil {
				return err
			}

			w := io.Writer(os.Stdout)
			if dryRun {
				fmt.Fprintln(w, "plan mode: nothing will be changed")
			}

			results, runErr := job.Run(ctx, cfg, w, job.Options{DryRun: dryRun})
			printSummary(w, results, dryRun)
			return runErr
		})
	},
}

func printSummary(w io.Writer, results []job.PairResult, plan bool) {
	if len(results) == 0 {
		return
	}
	heading := "transferred"
	if plan {
		heading = "would-xfer"
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%-20s %-8s %11s %8s %13s  %s\n",
		"pair", "variant", heading, "deleted", "deleted-dirs", "guard")
	for _, r := range results {
		guard := "ok"
		if r.GuardHit {
			guard = "BLOCKED"
		}
		fmt.Fprintf(w, "%-20s %-8s %11d %8d %13d  %s\n",
			r.Pair, r.Variant, r.Actual.Transferred, r.Actual.Deleted,
			r.Actual.DeletedDirs, guard)
	}
}
