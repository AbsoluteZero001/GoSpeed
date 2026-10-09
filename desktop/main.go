// Command gospeed-desktop is the Wails desktop client of GoSpeed.
//
// It is a presentation shell only: every measurement is executed by the same
// internal/speedtest engine the CLI uses. The GUI never starts a test on
// startup and never starts a listening test server.
package main

import (
	"embed"
	"fmt"
	"os"

	"github.com/AbsoluteZero001/GoSpeed/internal/version"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "GoSpeed " + version.Version,
		Width:     1240,
		Height:    840,
		MinWidth:  1024,
		MinHeight: 680,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 15, G: 18, B: 23, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "gospeed desktop: %v\n", err)
	}
}
