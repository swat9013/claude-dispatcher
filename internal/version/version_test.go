package version

import (
	"runtime/debug"
	"testing"
)

func TestDescribePrefersTheValuesLdflagsInjected(t *testing.T) {
	built := &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "fromvcs"}}}

	if got := describe("v0.3.0", "fromldflags", built).String(); got != "v0.3.0 (fromldflags)" {
		t.Fatalf("describe = %q", got)
	}
}

func TestDescribeShowsThePseudoVersionAndVCSRevisionOfAWorkingTreeBuild(t *testing.T) {
	// git の作業ツリーでの go build: Go が VCS から pseudo-version を刻み、vcs.revision を埋める
	built := &debug.BuildInfo{Main: debug.Module{Version: "v0.1.1-0.20260927163012-aac3e1c06160"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "aac3e1c0616017c41c4c52cf613d49e5d88a0643"}}}

	if got := describe("", "", built).String(); got != "v0.1.1-0.20260927163012-aac3e1c06160 (aac3e1c0616017c41c4c52cf613d49e5d88a0643)" {
		t.Fatalf("describe = %q", got)
	}
}

func TestDescribeShowsUnknownCommitWhenTheBuildHasNoVCSInformation(t *testing.T) {
	// go install <module>@<版> の build: module cache から build するので vcs.revision が無い
	built := &debug.BuildInfo{Main: debug.Module{Version: "v0.3.0"}}

	if got := describe("", "", built).String(); got != "v0.3.0 (unknown)" {
		t.Fatalf("describe = %q", got)
	}
}

func TestDescribeShowsDevelWhenNeitherLdflagsNorBuildInformationHaveTheValues(t *testing.T) {
	for name, built := range map[string]*debug.BuildInfo{"build 情報なし": nil, "版が空": {}} {
		t.Run(name, func(t *testing.T) {
			if got := describe("", "", built).String(); got != "(devel) (unknown)" {
				t.Fatalf("describe = %q", got)
			}
		})
	}
}
