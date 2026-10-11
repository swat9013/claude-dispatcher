package scaffold

import (
	"slices"
	"strings"
	"testing"
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
		"jira":   Jira("acme.atlassian.net", "WIDGETS"),
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
