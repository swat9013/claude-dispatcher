// Package version は binary の版と commit を返す (formats.md §14)。
package version

import "runtime/debug"

// version と commit は GoReleaser が ldflags (-X) で埋める (.goreleaser.yaml)。go install / go build では空
var (
	version string
	commit  string
)

// Info は binary の版と commit。
type Info struct {
	Version string
	Commit  string
}

// String は `--version` と doctor の版の行に出す綴り (`<版> (<commit>)`)。
func (i Info) String() string { return i.Version + " (" + i.Commit + ")" }

// Read は版と commit を返す。ldflags で埋まっていなければ build 情報から引く:
// 版は module の版 (go install <module>@<版> なら その版、手元の build なら "(devel)")、commit は vcs.revision。
// どちらからも引けなければ "(devel)" / "unknown"。
func Read() Info {
	info := Info{Version: version, Commit: commit}
	if built, ok := debug.ReadBuildInfo(); ok {
		if info.Version == "" {
			info.Version = built.Main.Version
		}
		for _, s := range built.Settings {
			if info.Commit == "" && s.Key == "vcs.revision" {
				info.Commit = s.Value
			}
		}
	}
	if info.Version == "" {
		info.Version = "(devel)"
	}
	if info.Commit == "" {
		info.Commit = "unknown"
	}
	return info
}
