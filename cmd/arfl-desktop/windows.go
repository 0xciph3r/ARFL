package main

import "github.com/wailsapp/wails/v3/pkg/application"

// Events the frontend listens for.
const (
	// eventOpen asks the main window to show, optionally opening a drawer
	// such as "topup", when the user acts from the tray popover.
	eventOpen = "arfl:open"
	// eventState tells every window that the connection changed, so the tray
	// popover and the main window never disagree.
	eventState = "arfl:state"
)

func (b *Bridge) attach(app *application.App, main, tray *application.WebviewWindow, setIcon func(bool)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.app, b.mainWin, b.trayWin, b.setTrayIcn = app, main, tray, setIcon
}

// ShowMain brings the main window forward and, when overlay is set, opens
// that drawer in it.
func (b *Bridge) ShowMain(overlay string) {
	b.mu.Lock()
	app, main, tray := b.app, b.mainWin, b.trayWin
	b.mu.Unlock()
	if main == nil {
		return
	}
	if tray != nil {
		tray.Hide()
	}
	main.Show().Focus()
	if overlay != "" && app != nil {
		app.Event.Emit(eventOpen, overlay)
	}
}

// stateChanged updates the tray icon and tells both windows to refresh.
func (b *Bridge) stateChanged(connected bool) {
	b.mu.Lock()
	app, setIcon := b.app, b.setTrayIcn
	b.mu.Unlock()
	if setIcon != nil {
		setIcon(connected)
	}
	if app != nil {
		app.Event.Emit(eventState, connected)
	}
}
