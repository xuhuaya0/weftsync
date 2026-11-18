// Package daemoncmd provides the `daemon` command: run jobs on a schedule with a
// local HTTP API.
package daemoncmd

import (
	"context"
	"errors"
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
	"weftsync/browser"
	"weftsync/console"
	"weftsync/daemon"
	"weftsync/tray"
)

var (
	addr        string
	logFile     string
	runNow      bool
	trayOn      bool
	noTray      bool
	openConsole bool
	hideConsole bool
	splitDir    string
)

// DefaultAddr is where the API listens unless told otherwise.
//
// Exported because main() needs the same value: on a double-click it asks this
// address whether weftsync is already running before starting a second copy.
// Two literals would drift, and the symptom would be a double-click that starts a
// doomed second instance instead of opening the console that is already there.
const DefaultAddr = "127.0.0.1:5572"

func init() {
	cmd.Root.AddCommand(commandDefinition)
	flags := commandDefinition.Flags()
	flags.StringVar(&addr, "addr", DefaultAddr,
		"Address for the local HTTP API (loopback only)")
	flags.StringVar(&logFile, "log-file", "",
		"Append the daemon log to this file instead of stdout")
	flags.BoolVar(&runNow, "run-now", false,
		"Run every enabled job once at startup, then follow the schedule")
	// On by default since 2026-11-18 (the user asked for it: 「默认开吧」).
	//
	// The icon is not decoration: it is the only place a scheduled run's failure
	// shows up while nobody is watching the screen. A daemon that runs unattended
	// and can only report into a log file nobody opens is the configuration this
	// default is meant to avoid.
	flags.BoolVar(&trayOn, "tray", true,
		"Show a notification area icon (default; Windows only)")
	flags.BoolVar(&noTray, "no-tray", false,
		"Do not show a notification area icon (for a console-only daemon)")
	// --open and --hide-console exist for the double-click case. With no arguments
	// main() starts this command with both set, so double-clicking the exe opens a
	// browser and gets out of the way instead of flashing a console window.
	flags.BoolVar(&openConsole, "open", false,
		"Open the console in a browser once the API is listening")
	flags.BoolVar(&hideConsole, "hide-console", false,
		"Hide this process's console window once the API is listening (Windows)")
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
		// Zero job files is a supported start.
		//
		// 2026-11-18, the user's objection: 「为什么还要准备文件啊，所有的操作不应该都在
		// GUI 进行么？不应该是 windows 一键启动，linux 一个命令启动」. They were right:
		// the console can create, save and schedule configurations, so requiring a
		// hand-written .toml before the console can be opened is a chicken-and-egg
		// that only exists because this check said 1.
		//
		// With no files the daemon serves the UI only; 新建 in the console asks
		// /api/config/new for a blank shape and 另存为 turns it into the first file.
		cmd.CheckArgs(0, 1e6, command, args)

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
					startupFailure("不能写日志文件：%v\n\n路径：%s", err, logFile)
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
				startupFailure("配置无法加载：%v", err)
				return err
			}

			api := &daemon.API{
				D: d, Addr: addr, Autostart: autostartSpec(args), SplitDir: splitDir,
				StartedAt: startedAt,
			}
			srv, ln, err := daemon.Listen(addr, api)
			if err != nil {
				// The realistic failure is "something already holds the port", and
				// after --hide-console there is no console left to read it from.
				if hideConsole {
					startupFailure("无法在 %s 上启动服务：%v\n\n"+
						"如果 weftsync 已经在运行，直接打开 http://%s/ 即可。", addr, err, addr)
				}
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

			// The server answers now, so the window that was only ever a liability
			// can go. Later failures show up in the console's 日志 page (the log sink
			// feeds it) and in --log-file, neither of which needs a console.
			if hideConsole {
				console.Hide()
			}

			// Open the console when asked (which a double-click implies; see
			// main.go). Done after the listener is up, so the page cannot load
			// before there is something to answer it - opening first produced a
			// browser error page that the user had to reload by hand.
			//
			// A failure here is reported and ignored: the URL is printed above,
			// and a headless machine has no browser to open.
			if openConsole {
				consoleURL := "http://" + ln.Addr().String() + "/"
				if err := browser.Open(consoleURL); err != nil {
					fmt.Fprintf(w, "weftsync: could not open a browser (%v); "+
						"the console is at %s\n", err, consoleURL)
				}
			}

			wantTray := wantsTray()
			if wantTray {
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
					// "Not supported on this platform" is not a problem to report:
					// the default is on, so every Linux run would otherwise open
					// with a line that reads like a fault. Real failures (no shell
					// window, Shell_NotifyIcon refused) still get printed.
					if !errors.Is(trayErr, tray.ErrUnsupported) {
						fmt.Fprintf(w, "tray: %v (continuing without an icon)\n", trayErr)
					}
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

// startupFailure reports a failure that happened before the server was serving.
//
// Only when the console has been asked to hide itself: at that point the error
// rclone is about to print goes into a window nobody can see, and the process
// exits, taking the window with it. A message box is the only thing left. In the
// ordinary terminal case the error is already on screen, so adding a modal box
// would make every typo a dialog to dismiss.
func startupFailure(format string, args ...any) {
	if !hideConsole {
		return
	}
	console.Errorf("weftsync 启动失败", format, args...)
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

// wantsTray resolves the two flags into one answer.
//
// Two flags for one switch because the default is ON: --no-tray has to be able to
// turn off what --tray turns on. --no-tray wins when both are given, because the
// only reason to type both is to be certain the icon stays away.
//
// One function rather than the expression written twice: the daemon decides whether
// to show the icon, and the autostart entry decides whether to record --no-tray, and
// those two must never disagree - an entry that omits the flag while the icon is
// off gives the icon back at the next login.
func wantsTray() bool { return trayOn && !noTray }

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
		Exe:      exe,
		JobFiles: jobFiles,
		Addr:     addr,
		LogFile:  logFile,
		// What has to survive a reboot is the NO (the icon is on by default), so
		// the field is named for the negative. wantsTray() is the single place
		// that resolves the two flags, and both the icon and the entry use it.
		NoTray:      !wantsTray(),
		ConfigFile:  cfgFile,
		RunNow:      runNow,
		DefaultAddr: "127.0.0.1:5572",
	})
	if err != nil {
		return autostart.Spec{}
	}
	return spec
}
