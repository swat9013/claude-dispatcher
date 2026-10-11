package scaffold

import (
	"strings"
	"testing"
)

func TestEveryTemplatePointsToTheGuideInItsFrontMatter(t *testing.T) {
	// 案内を指す comment は front matter の YAML の comment に置く。本文に書くと共通 prompt として worker に渡る
	templates := map[string]string{
		"github": GitHub("acme/widgets"),
		"gitlab": GitLab("gitlab.example.com", "acme/widgets"),
		"jira":   Jira("acme.atlassian.net", "WIDGETS"),
	}
	for kind, text := range templates {
		t.Run(kind, func(t *testing.T) {
			lines := strings.Split(text, "\n")

			second := lines[1]

			if lines[0] != "---" || !strings.HasPrefix(second, "# ") || !strings.Contains(second, "`claude-dispatcher guide`") {
				t.Fatalf("2 行目が front matter の中で案内を指す comment でない: %q", second)
			}
		})
	}
}
