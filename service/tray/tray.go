package tray

import (
	"os/exec"
	"runtime"
)

// OpenBrowser opens the default browser to the given URL.
func OpenBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "linux":
		return exec.Command("xdg-open", url).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// Note: fyne.io/systray integration requires the systray dependency.
// The full tray implementation (icon, menu, lifecycle) will be wired
// when the systray dependency is added. The core service runs without
// the tray (headless mode for testing/development).
