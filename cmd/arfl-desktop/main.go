// Command arfl-desktop is the cross-platform ARFL client.
//
// The window is a thin shell: all protocol work lives in internal/app, which
// this binary exposes to the frontend through Bridge. Keeping the logic out of
// the UI layer is what lets the same code back the CLI and, later, a
// privileged helper process.
package main

import (
	"embed"
	"log"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed trayicons
var trayIcons embed.FS

func trayIcon(name string) []byte {
	b, err := trayIcons.ReadFile("trayicons/" + name)
	if err != nil {
		log.Fatalf("arfl-desktop: tray icon %s: %v", name, err)
	}
	return b
}

func main() {
	bridge := NewBridge()

	app := application.New(application.Options{
		Name:        "ARFL",
		Description: "Two-hop bandwidth paid in bitcoin, with no accounts.",
		Services:    []application.Service{application.NewService(bridge)},
		Assets:      application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		Mac: application.MacOptions{
			// ARFL keeps running in the menu bar when its window is closed.
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
	})

	main := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "ARFL",
		Width:            960,
		Height:           640,
		MinWidth:         720,
		MinHeight:        600,
		BackgroundColour: application.NewRGB(13, 12, 16),
		URL:              "/",
		Mac: application.MacWindow{
			TitleBar:                application.MacTitleBarHiddenInset,
			InvisibleTitleBarHeight: 56,
		},
	})
	// Closing the window hides it; the tunnel and the tray stay up.
	main.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		main.Hide()
		e.Cancel()
	})

	popover := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "tray",
		Width:            340,
		Height:           470,
		Frameless:        true,
		AlwaysOnTop:      true,
		Hidden:           true,
		DisableResize:    true,
		HideOnEscape:     true,
		HideOnFocusLost:  true,
		BackgroundColour: application.NewRGB(13, 12, 16),
		URL:              "/#tray",
		Windows:          application.WindowsWindow{HiddenOnTaskbar: true},
	})
	popover.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		popover.Hide()
		e.Cancel()
	})

	tray := app.SystemTray.New()
	setTrayIcon := func(on bool) {
		state := "off"
		if on {
			state = "on"
		}
		if runtime.GOOS == "darwin" {
			tray.SetTemplateIcon(trayIcon("mac-" + state + ".png"))
		} else {
			tray.SetIcon(trayIcon("win-" + state + ".png"))
		}
	}
	setTrayIcon(false)
	tray.AttachWindow(popover).WindowOffset(5)

	menu := app.NewMenu()
	menu.Add("Open ARFL").OnClick(func(*application.Context) { bridge.ShowMain("") })
	menu.AddSeparator()
	menu.Add("Quit ARFL").OnClick(func(*application.Context) { app.Quit() })
	tray.SetMenu(menu)

	bridge.attach(app, main, popover, setTrayIcon)

	if err := app.Run(); err != nil {
		log.Fatalf("arfl-desktop: %v", err)
	}
}
