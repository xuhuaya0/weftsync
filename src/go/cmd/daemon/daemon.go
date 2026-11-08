// Package daemoncmd provides the `daemon` command: run jobs on a schedule with a
// local HTTP API.
package daemoncmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rclone/rclone/cmd"
	"github.com/spf13/cobra"

	"weftsync/autostart"
	"weftsync/daemon"
	"weftsync/tray"
)

var (
	addr     string
	logFile  string
	runNow   bool
	trayOn   bool
	splitDir string
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
	flags.BoolVar(&trayOn, "tray", false,
		"Show a notification area icon (Windows only; ignored elsewhere)")
	flags.StringVar(&splitDir, "split-dir", "",
		"Directory for split undo ledgers (default: next to the pair's state file)")
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

		// Captured before anything slow happens: the settings page shows
		// uptime, and starting the clock after loading the config would
		// understate it.
		startedAt := time.Now()

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

			api := &daemon.API{
				D: d, Addr: addr, Autostart: autostartSpec(args), SplitDir: splitDir,
				StartedAt: startedAt,
			}
			srv, ln, err := daemon.Listen(addr, api)
			if err != nil {
				return err
			}

			// Own the lifetime context separately from the signal handler: the
			// tray's "quit" item has to take the same shutdown path as Ctrl+C,
			// and a plain context cannot be triggered by anything but code.
			//
			// Signals are counted by hand rather than through
			// signal.NotifyContext, because the second signal has to mean
			// something different from the first. With NotifyContext every
			// further Ctrl+C is swallowed, so a user whose transfer is stuck has
			// no way out short of killing the process.
			base, cancel := context.WithCancel(context.Background())
			defer cancel()

			ctx, stopSignals := context.WithCancel(base)
			defer stopSignals()

			var signals = make(chan os.Signal, 2)
			signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(signals)

			go watchSignals(w, d, signals, cancel, stopSignals)

			fmt.Fprintf(w, "weftsync daemon: API listening on http://%s\n", ln.Addr())
			d.Start(ctx)

			if trayOn {
				// A tray that will not start is a missing convenience, not a
				// reason to refuse to sync.
				tr, trayErr := tray.Start(tray.Config{
					Tooltip:    "weftsync - 定时同步",
					ConsoleURL: "http://" + ln.Addr().String() + "/",
					OnRunAll: func() {
						for _, st := range d.Status() {
							if !st.Enabled {
								continue
							}
							// Trigger is asynchronous and ignores this context,
							// so the tray's message thread is not blocked by a
							// transfer.
							if err := d.Trigger(ctx, st.Name); err != nil {
								fmt.Fprintf(w, "tray run %s: %v\n", st.Name, err)
							}
						}
					},
					OnQuit:  cancel,
					OnError: func(err error) { fmt.Fprintln(w, err) },
				})
				if trayErr != nil {
					fmt.Fprintf(w, "tray: %v (continuing without an icon)\n", trayErr)
				} else {
					defer tr.Stop()
					fmt.Fprintln(w, "tray: icon shown; failures will raise a notification")
					// A scheduled run happens unattended, so failures have to
					// reach the user some other way. The notifier is set only
					// when the tray exists: without it there is nowhere to
					// show a balloon, and the log is the record.
					d.SetNotifier(func(title, text string) {
						if err := tr.Notify(title, text); err != nil {
							fmt.Fprintf(w, "tray notify: %v\n", err)
						}
					})
				}
			}

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

			// The message about what is being stopped was already printed by
			// watchSignals, at the moment the signal arrived. Repeating it here
			// would print it after the wait, which is the one time it is useless.
			d.Stop()
			fmt.Fprintln(w, "weftsync daemon: stopped")
			return nil
		})
	},
}

// watchSignals turns the first signal into a graceful stop and the second into
// an immediate exit.
//
// What the first signal actually does is worth stating precisely, because the
// documentation used to get it wrong: it cancels the daemon's context, and a
// running job inherits that context, so in-flight transfers are **interrupted**,
// not finished. Stop() then waits for them to return, which is fast but not
// instant (a multipart upload has to unwind).
//
// Reporting "waiting for X to finish" while also cancelling X was a lie. The
// message now says what happens, and a second signal is a real escape hatch for a
// stuck transfer rather than a swallowed keystroke.
func watchSignals(w io.Writer, d *daemon.Daemon, signals <-chan os.Signal,
	cancel, stopSignals context.CancelFunc) {
	<-signals
	running := d.RunningJobs()
	if len(running) == 0 {
		fmt.Fprintln(w, "weftsync daemon: shutting down")
	} else {
		fmt.Fprintf(w, "weftsync daemon: stopping %s; press Ctrl+C again to exit immediately\n",
			strings.Join(running, ", "))
	}
	cancel()
	stopSignals()

	<-signals
	fmt.Fprintln(w, "weftsync daemon: second signal, exiting without waiting")
	os.Exit(1)
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

	spec, err := autostart.SpecFrom(autostart.Invocation{
		Exe:         exe,
		JobFiles:    jobFiles,
		Addr:        addr,
		LogFile:     logFile,
		ConfigFile:  cfgFile,
		RunNow:      runNow,
		Tray:        trayOn,
		DefaultAddr: "127.0.0.1:5572",
	})
	if err != nil {
		return autostart.Spec{}
	}
	return spec
}
