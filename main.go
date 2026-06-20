// Novera — a local-first AI workbench (Wails v3 + React/TS).
//
// main wires the Go services to the webview window. Business logic lives in the
// internal/* packages; the structs registered here are the surface the frontend
// calls through generated TypeScript bindings.
package main

import (
	"embed"
	"log"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"

	"novera/internal/agent"
	"novera/internal/artifacts"
	"novera/internal/db"
	"novera/internal/gitsvc"
	"novera/internal/jobs"
	"novera/internal/llm"
	"novera/internal/secret"
	"novera/internal/settings"
	"novera/internal/terminal"
	"novera/internal/watcher"
	"novera/internal/workspace"
)

// Wails embeds everything under frontend/dist into the binary, so the shipped
// app is a single self-contained executable (no external web assets).
//
//go:embed all:frontend/dist
var assets embed.FS

func main() {
	if err := run(); err != nil {
		// Log and exit non-zero without an abrupt log.Fatal mid-stack, so any
		// deferred cleanup in run() can complete.
		log.Printf("Novera exited with error: %v", err)
		os.Exit(1)
	}
}

func run() error {
	ws := workspace.New()
	set := settings.New()
	secrets := secret.New()
	git := gitsvc.New(ws)
	dbsvc := db.New(secrets)
	jobsvc := jobs.New()
	artsvc := artifacts.New(ws)
	// Wire the watcher so the workspace's own atomic saves aren't reported back
	// to the UI as external changes.
	fsWatcher := watcher.New(ws)
	workspace.WireSelfWriteNotifier(ws, fsWatcher)
	// Let long data-tool ops surface as tracked jobs and register their outputs
	// as artifacts (with lineage back to the source file).
	workspace.WireJobsAndArtifacts(ws, jobsvc, artsvc)

	// mainWindow is assigned just below; the single-instance callback closes over
	// it to focus the already-running window when a second launch is attempted.
	var mainWindow *application.WebviewWindow

	app := application.New(application.Options{
		Name:        "Novera",
		Description: "Local-first AI workbench",
		// Only one Novera may run at a time — a second launch refocuses the first
		// instead of opening a duplicate window with its own service state.
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.novera.workbench",
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				if mainWindow != nil {
					mainWindow.UnMinimise()
					mainWindow.Show()
					mainWindow.Focus()
				}
			},
		},
		Services: []application.Service{
			application.NewService(ws),
			application.NewService(git),
			application.NewService(terminal.New(ws)),
			application.NewService(fsWatcher),
			application.NewService(llm.New(set, secrets)),
			application.NewService(agent.New(set, secrets, ws, git, dbsvc, jobsvc, artsvc)),
			application.NewService(dbsvc),
			application.NewService(jobsvc),
			application.NewService(artsvc),
			application.NewService(set),
			application.NewService(&SecretService{store: secrets}),
			application.NewService(&Shell{}),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		// On Windows (the primary target) and Linux the app already terminates
		// when its last window closes; this makes macOS match that close-to-quit
		// behavior so the lifecycle is consistent across platforms.
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	// Black title bar + dark window chrome on Windows.
	black := uint32(0x000000)
	titleText := uint32(0xC9D1D9)
	border := uint32(0x000000)
	darkBar := &application.WindowTheme{TitleBarColour: &black, TitleTextColour: &titleText, BorderColour: &border}

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{ //nolint:staticcheck // assigned to mainWindow below
		Name:             "main",
		Title:            "Novera",
		Width:            1280,
		Height:           820,
		MinWidth:         900,
		MinHeight:        600,
		BackgroundColour: application.NewRGB(13, 17, 23),
		DevToolsEnabled:  true,
		URL:              "/",
		Windows: application.WindowsWindow{
			Theme: application.Dark,
			CustomTheme: application.ThemeSettings{
				DarkModeActive:    darkBar,
				DarkModeInactive:  darkBar,
				LightModeActive:   darkBar,
				LightModeInactive: darkBar,
			},
		},
	})
	mainWindow = win

	// app.Run blocks until the app quits; returning the error lets main log it
	// and exit non-zero without an abrupt log.Fatal.
	return app.Run()
}
