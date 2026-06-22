package buildinfo

import (
	"fmt"
	"strings"
)

const ApplicationName = "Quarry Editor"

var (
	// These variables are populated by release builds through -ldflags.
	Version   = "0.0.0-dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

type Info struct {
	ApplicationName string
	Version         string
	Commit          string
	BuildDate       string
}

func Current() Info {
	return Info{
		ApplicationName: ApplicationName,
		Version:         clean(Version),
		Commit:          clean(Commit),
		BuildDate:       clean(BuildDate),
	}
}

func (i Info) OneLine() string {
	return fmt.Sprintf("%s %s (commit %s, built %s)", clean(i.ApplicationName), clean(i.Version), clean(i.Commit), clean(i.BuildDate))
}

func clean(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return strings.Join(strings.Fields(value), " ")
}
