package render_test

import (
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/render"
	"github.com/swat9013/claude-dispatcher/internal/target"
)

var vars = render.Vars{
	Issue:     target.Issue{Number: 42, Title: "Fix it", URL: "https://github.com/acme/widgets/issues/42", Labels: []string{"bug", "p1"}},
	Trigger:   "implement",
	Attempt:   2,
	Workspace: "/ws/issue-42",
}

func TestVariablesAreRenderedByTheirNames(t *testing.T) {
	got, err := render.Render("action", "{{ .issue.number }}|{{ .issue.title }}|{{ .issue.url }}|{{ .issue.labels }}|{{ .trigger.name }}|{{ .attempt }}|{{ .workspace }}", vars)

	if want := "42|Fix it|https://github.com/acme/widgets/issues/42|[bug p1]|implement|2|/ws/issue-42"; err != nil || got != want {
		t.Fatalf("描画 = %q (%v), want %q", got, err, want)
	}
}

func TestIssueWithoutLabelsRendersAnEmptyList(t *testing.T) {
	got, err := render.Render("action", "{{ len .issue.labels }}", render.Vars{})

	if err != nil || got != "0" {
		t.Fatalf("描画 = %q (%v), want 0", got, err)
	}
}

func TestUnknownVariableFailsToRender(t *testing.T) {
	_, err := render.Render("action", "{{ .issue.body }}", vars)

	if err == nil || !strings.Contains(err.Error(), "action の描画の失敗") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnknownFunctionFailsToRender(t *testing.T) {
	_, err := render.Render("action", "{{ upper .issue.title }}", vars)

	if err == nil || !strings.Contains(err.Error(), "upper") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckFindsUnknownVariablesInsideConditions(t *testing.T) {
	err := render.Check("本文", "{{ if .issue.labels }}{{ .issue.body }}{{ end }}")

	if err == nil {
		t.Fatal("条件の中の未知の変数を見落とした")
	}
}
