// Package version は binary の版と commit を返す (formats.md §14)。
package version

import "runtime/debug"

// version と commit は GoReleaser が ldflags (-X) で埋める (.goreleaser.yaml)。それ以外の build では空で、build 情報から引く
var (
	version string
	commit  string
)

// Build は binary の版と commit。どちらも空にならない (引けなければ "(devel)" / "unknown")。
type Build struct {
	Version string
	Commit  string
}

// String は `--version` の行に出す形 (`<版> (<commit>)`)。
func (b Build) String() string { return b.Version + " (" + b.Commit + ")" }

// Current は今の binary の版と commit を返す。`--version` と loop (log.jsonl の loop_start 行と状態 file) が同じ値を使う。
func Current() Build {
	built, _ := debug.ReadBuildInfo()
	return describe(version, commit, built)
}

// describe は ldflags の値を優先し、空なら build 情報 (built。無ければ nil) から引く:
// 版は module の版 (go install <module>@<版> ならその版、git の作業ツリーでの go build なら Go が刻む pseudo-version)、
// commit は vcs.revision (VCS の作業ツリーから build したときだけ在る)。どちらからも引けなければ "(devel)" / "unknown"。
func describe(ldVersion, ldCommit string, built *debug.BuildInfo) Build {
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
	return Build{Version: v, Commit: c}
}
