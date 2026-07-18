package buildinfo

import (
	"runtime"
	"runtime/debug"
	"strings"
)

const (
	ProductName       = "Novera"
	defaultVersion    = "0.1.0-dev"
	defaultChannel    = "development"
	unknownBuildValue = "unknown"
	maxBuildValueLen  = 256
)

var (
	// Release tasks populate these variables through -ldflags -X. They remain
	// variables (rather than constants) specifically so every package and the
	// About UI consume the same embedded identity.
	Version   = defaultVersion
	Commit    = unknownBuildValue
	BuildDate = unknownBuildValue
	Channel   = defaultChannel
)

// Info is safe, non-secret build metadata exposed to diagnostics and the UI.
type Info struct {
	ProductName  string `json:"productName"`
	Version      string `json:"version"`
	Commit       string `json:"commit"`
	BuildDate    string `json:"buildDate"`
	Channel      string `json:"channel"`
	Dirty        bool   `json:"dirty"`
	GoVersion    string `json:"goVersion"`
	WailsVersion string `json:"wailsVersion"`
}

// Current returns the embedded identity. When a development build was created
// without linker flags, available Go module/VCS metadata fills only unknown
// fields; explicit release values always win.
func Current() Info {
	info := Info{
		ProductName:  ProductName,
		Version:      clean(Version),
		Commit:       clean(Commit),
		BuildDate:    clean(BuildDate),
		Channel:      clean(Channel),
		GoVersion:    clean(runtime.Version()),
		WailsVersion: unknownBuildValue,
	}

	if build, ok := debug.ReadBuildInfo(); ok {
		if info.Version == defaultVersion && build.Main.Version != "" && build.Main.Version != "(devel)" {
			info.Version = clean(build.Main.Version)
		}
		for _, dependency := range build.Deps {
			if dependency.Path == "github.com/wailsapp/wails/v3" {
				info.WailsVersion = clean(dependency.Version)
				break
			}
		}
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				if info.Commit == unknownBuildValue {
					info.Commit = clean(setting.Value)
				}
			case "vcs.time":
				if info.BuildDate == unknownBuildValue {
					info.BuildDate = clean(setting.Value)
				}
			case "vcs.modified":
				info.Dirty = strings.EqualFold(strings.TrimSpace(setting.Value), "true")
			}
		}
	}

	return info
}

func clean(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" {
		return unknownBuildValue
	}
	if len(value) > maxBuildValueLen {
		return value[:maxBuildValueLen]
	}
	return value
}
