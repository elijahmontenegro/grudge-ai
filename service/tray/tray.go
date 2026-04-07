package tray

import (
	"os/exec"
	"runtime"

	"fyne.io/systray"
)

const spideyURL = "http://spidey.localhost:8420"

// Run starts the system tray. Blocks until quit is selected.
// Call from a goroutine — the main thread runs the HTTP server.
func Run(onQuit func()) {
	systray.Run(onReady, func() {
		if onQuit != nil {
			onQuit()
		}
	})
}

func onReady() {
	systray.SetTitle("Spidey")
	systray.SetTooltip("Spidey — RRC-Native Agentic Framework")

	mOpen := systray.AddMenuItem("Open Spidey", "Open in browser")
	mSettings := systray.AddMenuItem("Settings", "Configure providers")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit Spidey", "Stop the service")

	// Open browser on launch
	go OpenBrowser(spideyURL)

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				OpenBrowser(spideyURL)
			case <-mSettings.ClickedCh:
				OpenBrowser(spideyURL + "/settings")
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

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
