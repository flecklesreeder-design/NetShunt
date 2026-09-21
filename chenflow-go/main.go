package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	if !ensureSingleInstance() {
		killExistingInstance()
		closeSingleInstanceMutex()
		if !ensureSingleInstance() {
			singleInstanceWarnAndExit()
			return
		}
	}
	defer closeSingleInstanceMutex()

	if !ensureWebView2() {
		return
	}

	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "NetShunt",
		Width:     1200,
		Height:    850,
		Frameless: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 10, G: 10, B: 12, A: 1},
		OnStartup:        app.startup,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
