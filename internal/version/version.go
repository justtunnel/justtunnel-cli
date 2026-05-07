package version

import "runtime/debug"

// Set via -ldflags at build time. When unset (e.g. a local
// `go build ./...` without ldflags), init() below falls back to
// debug.ReadBuildInfo so dev binaries still report a meaningful
// commit/date instead of "unknown". See justtunnel-cli#51 (F-01).
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if Commit == "unknown" && setting.Value != "" {
				Commit = setting.Value
			}
		case "vcs.time":
			if Date == "unknown" && setting.Value != "" {
				Date = setting.Value
			}
		}
	}
}
