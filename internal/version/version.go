// Package version は binary の版と commit を返す (formats.md §14)。
package version

import "runtime/debug"

// version と commit は GoReleaser が ldflags (-X) で埋める (.goreleaser.yaml)。それ以外の build では空で、build 情報から引く
var (
	version string
	commit  string
)

// Line は `--version` と doctor の版の行に出す 1 行 (`<版> (<commit>)`) を返す。
func Line() string {
	built, _ := debug.ReadBuildInfo()
	return describe(version, commit, built)
}

// describe は ldflags の値を優先し、空なら build 情報 (built。無ければ nil) から引く:
// 版は module の版 (go install <module>@<版> ならその版、git の作業ツリーでの go build なら Go が刻む pseudo-version)、
// commit は vcs.revision (VCS の作業ツリーから build したときだけ在る)。どちらからも引けなければ "(devel)" / "unknown"。
func describe(ldVersion, ldCommit string, built *debug.BuildInfo) string {
	v, c := ldVersion, ldCommit
	if built != nil {
		if v == "" {
			v = built.Main.Version
		}
		for _, s := range built.Settings {
			if c == "" && s.Key == "vcs.revision" {
				c = s.Value
			}
		}
	}
	if v == "" {
		v = "(devel)"
	}
	if c == "" {
		c = "unknown"
	}
	return v + " (" + c + ")"
}
