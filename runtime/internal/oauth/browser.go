package oauth

import (
	"os/exec"
	"runtime"
)

// openBrowser launches the system browser to the given URL. Best-
// effort — on headless servers this fails and the caller surfaces
// the URL through the response instead.
func openBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		// Linux + the BSDs all ship xdg-open as part of xdg-utils.
		cmd = "xdg-open"
		args = []string{url}
	}
	return exec.Command(cmd, args...).Start()
}
