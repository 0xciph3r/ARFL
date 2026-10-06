package main

import (
	"fmt"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// OpenAtLogin reports whether ARFL starts when the user signs in. Wails
// registers a login item through SMAppService on macOS, the Run key on
// Windows and an XDG autostart entry on Linux.
func (b *Bridge) OpenAtLogin() bool {
	on, err := application.Get().Autostart.IsEnabled()
	return err == nil && on
}

// SetOpenAtLogin adds or removes ARFL from the user's login items.
func (b *Bridge) SetOpenAtLogin(on bool) error {
	am := application.Get().Autostart
	var err error
	if on {
		err = am.Enable()
	} else {
		err = am.Disable()
	}
	if err != nil {
		return fmt.Errorf("change open at login: %w", err)
	}
	return nil
}
