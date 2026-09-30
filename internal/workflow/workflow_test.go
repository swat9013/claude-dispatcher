package workflow_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// workflow 定義の読み込みと検査 (formats.md §2)。

const valid = `---
tracker:
  kind: github
  repo: acme/widgets
triggers:
  - name: implement
    on: issue
    when:
      labels:
        all: [ready-for-agent]
    action: |
      /implement
---
共通 prompt
`

func load(t *testing.T, content string, env map[string]string) (workflow.Definition, error) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "WORKFLOW.md")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return workflow.Load(file, func(key string) string { return env[key] })
}

// problems は Load の誤りを 1 件 1 行の列で返す。誤りが無ければ t を落とす。
func problems(t *testing.T, content string) []string {
	t.Helper()
	_, err := load(t, content, nil)
	if err == nil {
		t.Fatal("誤りを名指ししなかった")
	}
	return strings.Split(err.Error(), "\n")
}

func withTriggers(triggers string) string {
	return "---\ntracker:\n  kind: github\n  repo: acme/widgets\ntriggers:" + triggers + "\n---\n"
}

func TestValidDefinitionIsRead(t *testing.T) {
	def, err := load(t, valid, nil)

	if err != nil {
		t.Fatal(err)
	}
	if def.Tracker.Repo.String() != "acme/widgets" || def.Interval != workflow.DefaultInterval ||
		len(def.Triggers) != 1 || def.Triggers[0].Name != "implement" || def.Triggers[0].On != trigger.Issue ||
		strings.Join(def.Triggers[0].When.LabelsAll, ",") != "ready-for-agent" {
		t.Fatalf("読んだ定義 = %+v", def)
	}
}

func TestErrorsNameTheItem(t *testing.T) {
	cases := []struct {
		name     string
		workflow string
		names    []string
	}{
		{"top level の未知の key", strings.Replace(valid, "tracker:", "trackr:\n  kind: github\ntracker:", 1), []string{"trackr"}},
		{"入れ子の未知の key", strings.Replace(valid, "all: [ready-for-agent]", "al: [ready-for-agent]", 1), []string{"triggers[0].when.labels.al"}},
		{"trigger の未知の key", strings.Replace(valid, "    action: |", "    acton: x\n    action: |", 1), []string{"triggers[0].acton"}},
		{"必須の項目の欠落", strings.Replace(valid, "  repo: acme/widgets\n", "", 1), []string{"tracker.repo"}},
		{"未知の tracker", strings.Replace(valid, "kind: github", "kind: jira", 1), []string{"tracker.kind", "jira"}},
		{"repo の綴りの誤り", strings.Replace(valid, "repo: acme/widgets", "repo: widgets", 1), []string{"tracker.repo", "widgets"}},
		{"未知の作業対象の種類", strings.Replace(valid, "on: issue", "on: pr", 1), []string{"triggers[0].on", "pr"}},
		{"空白だけの action", strings.Replace(valid, "    action: |\n      /implement\n", "    action: \" \"\n", 1), []string{"triggers[0].action"}},
		{"action の欠落", strings.Replace(valid, "    action: |\n      /implement\n", "", 1), []string{"triggers[0].action"}},
		{"型の誤り", strings.Replace(valid, "all: [ready-for-agent]", "all: ready-for-agent", 1), []string{"triggers[0].when.labels.all"}},
		{"未知の作者の立場", strings.Replace(valid, "        all: [ready-for-agent]", "        all: [ready-for-agent]\n      author: owner", 1), []string{"triggers[0].when.author", "owner"}},
		{"assignee と unassigned の併記", strings.Replace(valid, "        all: [ready-for-agent]", "        all: [ready-for-agent]\n      assignee: alice\n      unassigned: true", 1), []string{"triggers[0].when"}},
		{"空の labels.any", strings.Replace(valid, "all: [ready-for-agent]", "any: []", 1), []string{"triggers[0].when.labels.any"}},
		{"空の assignee", strings.Replace(valid, "        all: [ready-for-agent]", "        all: [ready-for-agent]\n      assignee: \"\"", 1), []string{"triggers[0].when.assignee"}},
		{"空の milestone", strings.Replace(valid, "        all: [ready-for-agent]", "        all: [ready-for-agent]\n      milestone: \"\"", 1), []string{"triggers[0].when.milestone"}},
		{"空の label", strings.Replace(valid, "all: [ready-for-agent]", "all: [\"\"]", 1), []string{"triggers[0].when.labels.all[0]"}},
		{"trigger の名前の重複", withTriggers("\n  - {name: t, on: issue, action: /a}\n  - {name: t, on: issue, action: /b}"), []string{"triggers[1].name", "t"}},
		{"trigger の名前の綴り", withTriggers("\n  - {name: \"a b\", on: issue, action: /a}"), []string{"triggers[0].name"}},
		{"trigger が 1 つも無い", withTriggers(" []"), []string{"triggers"}},
		{"周期の下限の外", strings.Replace(valid, "triggers:", "polling:\n  interval: 999ms\ntriggers:", 1), []string{"polling.interval"}},
		{"周期の上限の外", strings.Replace(valid, "triggers:", "polling:\n  interval: 24h1s\ntriggers:", 1), []string{"polling.interval"}},
		{"周期の綴り", strings.Replace(valid, "triggers:", "polling:\n  interval: soon\ntriggers:", 1), []string{"polling.interval"}},
		{"同じ key の 2 回目", strings.Replace(valid, "  kind: github\n", "  kind: github\n  kind: github\n", 1), []string{"tracker.kind"}},
		{"token に値そのもの", strings.Replace(valid, "  repo: acme/widgets\n", "  repo: acme/widgets\n  token: ghp_abc\n", 1), []string{"tracker.token"}},
		{"$VAR の未設定", strings.Replace(valid, "repo: acme/widgets", "repo: $ISSUE_REPO", 1), []string{"tracker.repo", "ISSUE_REPO"}},
		{"YAML として読めない", strings.Replace(valid, "tracker:", "tracker: [", 1), []string{"YAML"}},
		{"front matter が無い", "共通 prompt だけ\n", []string{"front matter"}},
		{"front matter が閉じていない", "---\ntracker:\n  kind: github\n", []string{"front matter"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Join(problems(t, c.workflow), "\n")

			for _, name := range c.names {
				if !strings.Contains(got, name) {
					t.Fatalf("誤りが %q を名指ししていない:\n%s", name, got)
				}
			}
		})
	}
}

func TestIntervalAtTheBoundsIsAccepted(t *testing.T) {
	for _, interval := range []string{"1s", "24h"} {
		def, err := load(t, strings.Replace(valid, "triggers:", "polling:\n  interval: "+interval+"\ntriggers:", 1), nil)
		if err != nil {
			t.Fatalf("%s: %v", interval, err)
		}
		if want, _ := time.ParseDuration(interval); def.Interval != want {
			t.Fatalf("周期 = %s, want %s", def.Interval, want)
		}
	}
}

func TestWhenWithoutAValueIsReadAsNoCondition(t *testing.T) {
	def, err := load(t, withTriggers("\n  - name: all\n    on: issue\n    when:\n    action: /all"), nil)

	if err != nil {
		t.Fatal(err)
	}
	if def.Triggers[0].When.LabelsAll != nil || def.Triggers[0].When.Assignee != "" {
		t.Fatalf("述語 = %+v, want 条件なし", def.Triggers[0].When)
	}
}

func TestAnchoredValuesCanBeSharedWithAliases(t *testing.T) {
	def, err := load(t, withTriggers(`
  - name: a
    on: issue
    when: {labels: &ready {all: [ready]}}
    action: /a
  - name: b
    on: issue
    when: {labels: *ready}
    action: /b`), nil)

	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(def.Triggers[1].When.LabelsAll, ",") != "ready" {
		t.Fatalf("alias の述語 = %+v", def.Triggers[1].When)
	}
}

func TestVariableIsReplacedWithTheEnvironmentValue(t *testing.T) {
	content := strings.Replace(valid, "  repo: acme/widgets\n", "  repo: $ISSUE_REPO\n  token: $WIDGETS_TOKEN\n", 1)

	def, err := load(t, content, map[string]string{"ISSUE_REPO": "other/gadgets", "WIDGETS_TOKEN": "secret-1"})

	if err != nil {
		t.Fatal(err)
	}
	if def.Tracker.Repo.String() != "other/gadgets" || def.Tracker.Token != "secret-1" {
		t.Fatalf("tracker = %+v", def.Tracker)
	}
}

func TestMalformedRepoFromAVariableIsNamedWithoutItsValue(t *testing.T) {
	_, err := load(t, strings.Replace(valid, "repo: acme/widgets", "repo: $ISSUE_REPO", 1), map[string]string{"ISSUE_REPO": "not-a-repo"})

	if err == nil || !strings.Contains(err.Error(), "tracker.repo") || strings.Contains(err.Error(), "not-a-repo") {
		t.Fatalf("err = %v, want tracker.repo を名指しし、環境変数の値は載せない", err)
	}
}

// missingItems は誤りのうち「必須の項目が無い」ものを、file 名を除いて返す。
func missingItems(t *testing.T, content string) []string {
	t.Helper()
	var missing []string
	for _, p := range problems(t, content) {
		if strings.Contains(p, "必須の項目が無い") {
			missing = append(missing, p[strings.Index(p, ": ")+2:])
		}
	}
	return missing
}

const trackerWithoutKindAndRepo = "---\npolling:\n  interval: 5m\ntracker:\n  token: $T\ntriggers:\n  - {name: a, on: issue, action: /a}\n---\n"

func TestMissingRequiredItemIsNamedAtTheLineOfItsParentKey(t *testing.T) {
	got := missingItems(t, trackerWithoutKindAndRepo)

	for _, m := range got {
		if !strings.Contains(m, "(4 行目)") {
			t.Fatalf("必須の欠落 %q が tracker: の行 (4 行目) を指していない", m)
		}
	}
}

func TestMissingRequiredItemsOfOneMappingAreNamedInNameOrder(t *testing.T) {
	got := missingItems(t, trackerWithoutKindAndRepo)

	if len(got) != 2 || !strings.HasPrefix(got[0], "tracker.kind") || !strings.HasPrefix(got[1], "tracker.repo") {
		t.Fatalf("必須の欠落 = %q, want tracker.kind, tracker.repo の順", got)
	}
}

func TestBodyIsReadAsTheCommonPrompt(t *testing.T) {
	def, err := load(t, valid, nil)

	if err != nil || def.Prompt != "共通 prompt\n" {
		t.Fatalf("本文 = %q (%v)", def.Prompt, err)
	}
}

func TestYAMLSyntaxErrorNamesTheLineInTheFile(t *testing.T) {
	// front matter の 2 行目 (file の 3 行目) が tab で字下げしている
	got := problems(t, "---\ntracker:\n\tkind: github\n---\n")

	if !strings.Contains(got[0], "yaml: line 3:") {
		t.Fatalf("誤り = %q, want file の行番号 (line 3)", got[0])
	}
}
