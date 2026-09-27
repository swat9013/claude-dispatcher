package version

import (
	"runtime/debug"
	"testing"
)

func TestDescribePrefersTheValuesLdflagsInjected(t *testing.T) {
	built := &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "fromvcs"}}}

	if got := describe("v0.3.0", "fromldflags", built); got != "v0.3.0 (fromldflags)" {
		t.Fatalf("describe = %q", got)
	}
}

func TestDescribeFallsBackToTheModuleVersionAndVCSRevision(t *testing.T) {
	built := &debug.BuildInfo{Main: debug.Module{Version: "v0.3.0"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}}}

	if got := describe("", "", built); got != "v0.3.0 (abc123)" {
		t.Fatalf("describe = %q", got)
	}
}

func TestDescribeShowsUnknownCommitWhenTheBuildHasNoVCSInformation(t *testing.T) {
	// go install <module>@<版> の build: module cache から build するので vcs.revision が無い
	built := &debug.BuildInfo{Main: debug.Module{Version: "v0.3.0"}}

	if got := describe("", "", built); got != "v0.3.0 (unknown)" {
		t.Fatalf("describe = %q", got)
	}
}

func TestDescribeShowsDevelWhenNeitherLdflagsNorBuildInformationHaveTheValues(t *testing.T) {
	for name, built := range map[string]*debug.BuildInfo{"build 情報なし": nil, "版が空": {}} {
		if got := describe("", "", built); got != "(devel) (unknown)" {
			t.Fatalf("%s: describe = %q", name, got)
		}
	}
}
