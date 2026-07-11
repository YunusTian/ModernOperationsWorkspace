// Command mow-desktop 是 MOW 的桌面客户端入口（Wails v2）。
// UI 只通过 core/command 引擎与内核交互，不承载业务逻辑。
package main

import (
	"context"
	"embed"
	"log"

	"github.com/mow/mow/sdk/version"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app, err := NewApp()
	if err != nil {
		log.Fatalf("desktop: init app: %v", err)
	}

	err = wails.Run(&options.App{
		Title:  "MOW Desktop " + version.Version,
		Width:  1280,
		Height: 800,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup: func(ctx context.Context) {
			app.SetContext(ctx)
		},
		OnShutdown: func(ctx context.Context) {
			app.Close()
		},
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		log.Fatalf("desktop: wails run: %v", err)
	}
}
