package browser

import (
	"os/exec"
	"runtime"
)

// Open shows url in the user's default browser.
//
// It returns an error instead of failing loudly: a daemon that cannot open a
// browser is still a working daemon, and the console URL is printed either way.
// The caller decides whether that is worth mentioning.
func Open(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// rundll32 hands the URL to the registered protocol handler without a
		// console window and without the quoting problems `cmd /c start` has
		// with & in a query string.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
