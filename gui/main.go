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
	// Create an instance of the app structure
	app := NewApp()

	// Create application with options
	err := wails.Run(&options.App{
		Title:     "Heroku",
		Width:     1180,
		MinWidth:  720,
		Height:    760,
		MinHeight: 480,
		// Frameless — своя шапка (трафик-лайты + перетаскивание через
		// --wails-draggable в CSS) вместо системной рамки окна. .topbar уже
		// был размечен под это (-webkit-app-region: drag), просто раньше не
		// был включён здесь — рисовать поверх системной рамки ещё и свою
		// было бы дублирующимся чужеродным элементом.
		Frameless: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 13, G: 17, B: 23, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
