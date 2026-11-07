// Package daemoncmd provides the `daemon` command: run jobs on a schedule with a
// local HTTP API.
package daemoncmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/rclone/rclone/cmd"
	"github.com/spf13/cobra"

	"weftsync/autostart"
	"weftsync/daemon"
)

var (
	addr    string
	logFile string
	runNow  bool
)

func init() {
	cmd.Root.AddCommand(commandDefinition)
	flags := commandDefinition.Flags()
	flags.StringVar(&addr, "addr", "127.0.0.1:5572",
		"Address for the local HTTP API (loopback only)")
	flags.StringVar(&logFile, "log-file", "",
		"Append the daemon log to this file instead of stdout")
	flags.BoolVar(&runNow, "run-now", false,
		"Run every enabled job once at startup, then follow the schedule")
}

var commandDefinition = &cobra.Command{
	Use:   "daemon job.toml [job2.toml...]",
	Short: `Run job files on a schedule, with a local HTTP API`,
	Long: `Runs one or more job files unattended.

Each job file may carry a [job.schedule] table:

  mode = "interval"     every schedule.interval, optionally phase-locked to
                        schedule.start_at when align_to_start = true
  mode = "daily_time"   once a day at schedule.daily_time
  mode = "cron"         a standard five-field cron expression
  (absent)              manual only - never fires on its own

The delete guard, versioning and conflict policy of each pair all still apply:
the daemon only decides when a job runs, not whether it is allowed to delete.

A local HTTP API is started for the UI:

  GET  /api/status                 every job, its state and last result
  POST /api/run?job=NAME           start one job
  POST /api/run                    start every enabled job
  GET  /api/health                 liveness

It binds to 127.0.0.1 only. The API can start syncs and carries no
authentication, so a non-loopback address is refused rather than risked.

Binding the port doubles as the single-instance guard: a second daemon on the
same address fails immediately instead of running a second scheduler over the
same pairs.

Stop with Ctrl+C; a run in progress is allowed to finish.
`,
	Run: func(command *cobra.Command, args []string) {
		cmd.CheckArgs(1, 1e6, command, args)

		cmd.Run(false, false, command, func() error {
			w := io.Writer(os.Stdout)
			var closer io.Closer
			if logFile != "" {
				f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
				if err != nil {
					return fmt.Errorf("open log file: %w", err)
				}
				w = f
				closer = f
			}
			if closer != nil {
				defer func() { _ = closer.Close() }()
			}

			// Tee the log so the UI can show recent lines: the daemon may be
			// writing to a file, but the page still needs something to display.
			sink := daemon.NewLogSink(w)
			d, err := daemon.New(args, sink)
			if err != nil {
				return err
			}

			api := &daemon.API{D: d, Addr: addr, Autostart: autostartSpec(args)}
			srv, ln, err := daemon.Listen(addr, api)
			if err != nil {
				return err
			}

			ctx, stopSignals := signal.NotifyContext(context.Background(),
				os.Interrupt, syscall.SIGTERM)
			defer stopSignals()

			fmt.Fprintf(w, "weftsync daemon: API listening on http://%s\n", ln.Addr())
			d.Start(ctx)

			if runNow {
				for _, st := range d.Status() {
					if !st.Enabled {
						continue
					}
					if err := d.Trigger(ctx, st.Name); err != nil {
						fmt.Fprintf(w, "run-now %s: %v\n", st.Name, err)
					}
				}
			}

			// Serve blocks until the signal fires, then shuts down gracefully.
			if err := daemon.Serve(ctx, srv, ln); err != nil {
				return err
			}
			d.Stop()
			fmt.Fprintln(w, "weftsync daemon: stopped")
			return nil
		})
	},
}

// autostartSpec captures how this process was started, so "enable autostart" in
// the UI reproduces the invocation the user has working rather than asking them
// to retype it.
//
// A failure here is not fatal: the daemon still works, only the login-startup
// toggle stays off (the API reports it as unavailable).
func autostartSpec(jobFiles []string) autostart.Spec {
	exe, err := os.Executable()
	if err != nil {
		return autostart.Spec{}
	}
	// --config is rclone's, so it is a persistent flag on the root command, not
	// one of ours; read it back from where cobra keeps it.
	cfgFile := ""
	if f := cmd.Root.PersistentFlags().Lookup("config"); f != nil && f.Changed {
		cfgFile = f.Value.String()
	}

	spec, err := autostart.SpecFromInvocation(exe, jobFiles, addr, logFile, cfgFile,
		runNow, "127.0.0.1:5572")
	if err != nil {
		return autostart.Spec{}
	}
	return spec
}
