package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os/exec"
	"runtime"

	"fyne.io/systray"
)

const grudgeURL = "http://grudge.localhost:8420"

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
	systray.SetIcon(spiderIcon())
	systray.SetTitle("Grudge")
	systray.SetTooltip("Grudge — RRC-Native Agentic Framework")

	mOpen := systray.AddMenuItem("Open Grudge", "Open in browser")
	mSettings := systray.AddMenuItem("Settings", "Configure providers")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit Grudge", "Stop the service")

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				OpenBrowser(grudgeURL)
			case <-mSettings.ClickedCh:
				OpenBrowser(grudgeURL + "/settings")
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

// spiderIcon generates a 32x32 ICO-format spider icon for Windows systray.
func spiderIcon() []byte {
	const size = 32
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	purple := color.RGBA{124, 58, 237, 255}

	set := func(x, y int) {
		if x >= 0 && x < size && y >= 0 && y < size {
			img.SetRGBA(x, y, purple)
			if x+1 < size {
				img.SetRGBA(x+1, y, purple)
			}
			if y+1 < size {
				img.SetRGBA(x, y+1, purple)
			}
		}
	}

	// Body
	for x := 13; x <= 19; x++ {
		for y := 12; y <= 20; y++ {
			dx := float64(x) - 16
			dy := float64(y) - 16
			if dx*dx/9+dy*dy/16 <= 1.2 {
				set(x, y)
			}
		}
	}
	// Head
	for x := 14; x <= 18; x++ {
		for y := 8; y <= 12; y++ {
			dx := float64(x) - 16
			dy := float64(y) - 10
			if dx*dx/4+dy*dy/4 <= 1.2 {
				set(x, y)
			}
		}
	}
	// Eyes
	img.SetRGBA(15, 9, color.RGBA{255, 255, 255, 255})
	img.SetRGBA(17, 9, color.RGBA{255, 255, 255, 255})
	// Legs
	legs := [][2]int{{12, 13}, {10, 11}, {9, 15}, {10, 19}}
	for i, end := range legs {
		startY := 13 + i*2
		for step := 0; step <= 5; step++ {
			set(13-step*(13-end[0])/5, startY+step*(end[1]-startY)/5)
		}
	}
	rlegs := [][2]int{{20, 13}, {22, 11}, {23, 15}, {22, 19}}
	for i, end := range rlegs {
		startY := 13 + i*2
		for step := 0; step <= 5; step++ {
			set(19+step*(end[0]-19)/5, startY+step*(end[1]-startY)/5)
		}
	}

	// Encode as ICO (Windows requires this, not PNG)
	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, img)
	pngData := pngBuf.Bytes()

	// ICO header: 6 bytes header + 16 bytes directory entry + PNG data
	ico := make([]byte, 0, 22+len(pngData))
	// ICONDIR header
	ico = append(ico, 0, 0)       // reserved
	ico = append(ico, 1, 0)       // type: 1 = icon
	ico = append(ico, 1, 0)       // count: 1 image
	// ICONDIRENTRY
	ico = append(ico, byte(size)) // width
	ico = append(ico, byte(size)) // height
	ico = append(ico, 0)          // color palette
	ico = append(ico, 0)          // reserved
	ico = append(ico, 1, 0)       // color planes
	ico = append(ico, 32, 0)      // bits per pixel
	// size of PNG data (little-endian uint32)
	pngLen := uint32(len(pngData))
	ico = append(ico, byte(pngLen), byte(pngLen>>8), byte(pngLen>>16), byte(pngLen>>24))
	// offset to PNG data (little-endian uint32) = 22
	ico = append(ico, 22, 0, 0, 0)
	// PNG data
	ico = append(ico, pngData...)
	return ico
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
