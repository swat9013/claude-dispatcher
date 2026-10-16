package scaffold

import (
	"os"
	"path/filepath"
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

// guideExample は案内の「workflow 定義の書き方」の節の最初の fenced block (書式例) の行。節は下位の節を含むので、
// 下位の節に例が増えても書式例を取り違えないよう、最初の block に限る。block が無ければ test を落とす。
func guideExample(t *testing.T) []string {
	t.Helper()
	s, err := guide.FindSection(guide.Text(), "workflow 定義の書き方")
	if err != nil {
		t.Fatal(err)
	}
	blocks := s.FencedBlocks()
	if len(blocks) == 0 {
		t.Fatalf("案内の節 %q に書式例 (fenced block) が無い", s.Heading)
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

// reentryNote は案内の項目の表の下にある、再入の仕組みを塞ぐ理由と見分け方の注記の見出し
const reentryNote = "再入の仕組みを塞ぐ"

// reentrySection は注記を置く案内の節 (項目の表の節) の見出し
const reentrySection = "項目"

func TestEveryTemplateAndTheGuideExamplePointToTheReentryNote(t *testing.T) {
	// 注記の見出しを変えて片方だけ直すと、雛形のコメントが案内に無い置き場を指す
	items, err := guide.FindSection(guide.Text(), reentrySection)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(items.Body, "**"+reentryNote+"**:") {
		t.Fatalf("案内の節 %q に注記 %q が無い", items.Heading, reentryNote)
	}
	repoWorkflow, err := os.ReadFile(filepath.Join("..", "..", "WORKFLOW.md"))
	if err != nil {
		t.Fatal(err)
	}
	pointer := reentrySection + "の表の下の注記「" + reentryNote + "」"
	texts := map[string]string{
		"guide example":    strings.Join(guideExample(t), "\n"),
		"repo WORKFLOW.md": string(repoWorkflow),
		"github":           GitHub("acme/widgets"),
		"gitlab":           GitLab("gitlab.example.com", "acme/widgets"),
		"jira":             Jira("acme.atlassian.net", "WIDGETS", GitLabCL("gitlab.example.com", "acme/widgets")),
	}
	for name, text := range texts {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(text, pointer) {
				t.Fatalf("args のコメントが注記 %q を指さない", reentryNote)
			}
		})
	}
}
