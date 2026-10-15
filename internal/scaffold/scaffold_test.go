package scaffold

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/guide"
)

// frontMatter は雛形の 1 行目の `---` から次の `---` の行までの行。閉じていなければ nil。
func frontMatter(text string) []string {
	lines := strings.Split(text, "\n")
	if lines[0] != "---" {
		return nil
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return lines[1:i]
		}
	}
	return nil
}

func TestEveryTemplatePointsToTheGuideInItsFrontMatter(t *testing.T) {
	// 案内を指す comment は front matter の YAML の comment に置く。本文に書くと共通 prompt として worker に渡る
	templates := map[string]string{
		"github": GitHub("acme/widgets"),
		"gitlab": GitLab("gitlab.example.com", "acme/widgets"),
		"jira":   Jira("acme.atlassian.net", "WIDGETS", GitLabCL("gitlab.example.com", "acme/widgets")),
	}
	for kind, text := range templates {
		t.Run(kind, func(t *testing.T) {
			lines := frontMatter(text)
			if lines == nil {
				t.Fatalf("雛形に front matter が無いか、閉じていない:\n%s", text)
			}
			pointsToGuide := func(line string) bool {
				return strings.HasPrefix(strings.TrimSpace(line), "#") && strings.Contains(line, "`claude-dispatcher guide`")
			}

			found := slices.ContainsFunc(lines, pointsToGuide)

			if !found {
				t.Fatalf("front matter に案内を指す comment が無い:\n%s", strings.Join(lines, "\n"))
			}
		})
	}
}

// argsLine は行の列のうち `args: [` で始まる唯一の行 (前後の空白を除く)。無いか複数あれば test を落とす。
func argsLine(t *testing.T, lines []string) string {
	t.Helper()
	var found []string
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "args: [") {
			found = append(found, trimmed)
		}
	}
	if len(found) != 1 {
		t.Fatalf("args の行がちょうど 1 つでない: %q", found)
	}
	return found[0]
}

// guideExample は案内の「workflow 定義の書き方」の節の書式例 (fenced block) の行。書式例がちょうど 1 つでなければ test を落とす。
func guideExample(t *testing.T) []string {
	t.Helper()
	s, err := guide.FindSection(guide.Text(), "workflow 定義の書き方")
	if err != nil {
		t.Fatal(err)
	}
	blocks := s.FencedBlocks()
	if len(blocks) != 1 {
		t.Fatalf("案内の節 %q の書式例がちょうど 1 つでない: %d", s.Heading, len(blocks))
	}
	return blocks[0].Lines
}

// endsWithDisallowedTools は列が `--disallowedTools` の値で終わること。--disallowedTools は値を複数取るので、
// 後ろに別の引数を置くと値に呑まれる。dispatcher が後ろに渡す -p で止まる
var endsWithDisallowedTools = regexp.MustCompile(`--disallowedTools, "[^"]+"\]$`)

func TestEveryTemplateBlocksTheSameReentryToolsAsTheGuideExample(t *testing.T) {
	// 再入の tool の名前の列を直すときに片方だけ直すと、setup が案内の勧めと食い違う雛形を書く
	want := argsLine(t, guideExample(t))
	if !endsWithDisallowedTools.MatchString(want) {
		t.Fatalf("案内の書式例の args が --disallowedTools の値で終わらない: %s", want)
	}
	templates := map[string]string{
		"github": GitHub("acme/widgets"),
		"gitlab": GitLab("gitlab.example.com", "acme/widgets"),
		"jira":   Jira("acme.atlassian.net", "WIDGETS", GitLabCL("gitlab.example.com", "acme/widgets")),
	}
	for kind, text := range templates {
		t.Run(kind, func(t *testing.T) {
			got := argsLine(t, frontMatter(text))

			if got != want {
				t.Fatalf("雛形の args が案内の書式例と違う:\n雛形: %s\n案内: %s", got, want)
			}
		})
	}
}
