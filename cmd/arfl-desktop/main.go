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

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	bridge := NewBridge()

	err := wails.Run(&options.App{
		Title:            "ARFL",
		Width:            420,
		Height:           680,
		MinWidth:         380,
		MinHeight:        560,
		BackgroundColour: &options.RGBA{R: 12, G: 14, B: 20, A: 1},
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        bridge.Startup,
		OnShutdown:       bridge.Shutdown,
		Bind:             []any{bridge},
		Mac: &mac.Options{
			TitleBar: mac.TitleBarHiddenInset(),
			About: &mac.AboutInfo{
				Title:   "ARFL",
				Message: "Decentralised VPN powered by Bitcoin Lightning.",
			},
		},
	})
	if err != nil {
		log.Fatalf("arfl-desktop: %v", err)
	}
}
