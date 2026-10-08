package proc_test

import (
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/proc"
)

func TestErrorTextLeavesOutAnHTMLBodyButKeepsTheCommandAndTheExitCode(t *testing.T) {
	for name, stderr := range map[string]string{
		"glab repo view の 502": "  Get https://gitlab.example.com/api/v4/projects/acme%2Fw: 502 failed to parse unknown error format: <html>\n" +
			"  <head><title>502 Bad Gateway</title></head>\n  <body><h1>502 Bad Gateway</h1></body>\n  </html>\n  .\n",
		"doctype から始まる本文": "HTTP 401: Unauthorized\n<!DOCTYPE html>\n<HTML><body><h1>Sign in</h1></body></HTML>\n",
	} {
		t.Run(name, func(t *testing.T) {
			err := &proc.Error{Name: "glab", Args: []string{"repo", "view", "--output", "json"}, Exit: 1, Stderr: stderr}

			text := err.Error()

			for _, want := range []string{"glab repo view", "exit 1", "HTML の本文を省いた"} {
				if !strings.Contains(text, want) {
					t.Errorf("%q を含まない: %s", want, text)
				}
			}
			for _, body := range []string{"<html>", "<HTML>", "<body>", "<!DOCTYPE", "<h1>"} {
				if strings.Contains(text, body) {
					t.Errorf("本文 %q が載った: %s", body, text)
				}
			}
		})
	}
}

func TestErrorTextKeepsWhatComesBeforeTheHTMLBody(t *testing.T) {
	stderr := "  Get https://gitlab.example.com/api/v4/projects/acme%2Fw: 502 failed to parse unknown error format: <html><body>x</body></html>\n"
	err := &proc.Error{Name: "glab", Args: []string{"repo", "view"}, Exit: 1, Stderr: stderr}

	text := err.Error()

	if !strings.Contains(text, "Get https://gitlab.example.com/api/v4/projects/acme%2Fw: 502") {
		t.Fatalf("撃った要求と status が残らない: %s", text)
	}
}

func TestErrorTextKeepsAStderrWithoutHTMLWhole(t *testing.T) {
	stderr := "GraphQL: Could not resolve to an Issue with the number of 42. (repository.issue)\n"
	err := &proc.Error{Name: "gh", Args: []string{"api", "graphql"}, Exit: 1, Stderr: stderr}

	text := err.Error()

	if !strings.Contains(text, strings.TrimSpace(stderr)) || strings.Contains(text, "省いた") {
		t.Fatalf("stderr がそのまま載らない: %s", text)
	}
}

func TestSummaryTakesThePlaceOfTheStderrInTheErrorText(t *testing.T) {
	err := &proc.Error{Name: "glab", Args: []string{"api", "--hostname"}, Exit: 1, Stderr: "glab: HTTP 403\n", Summary: "短い理由"}

	text := err.Error()

	if !strings.Contains(text, "短い理由") || strings.Contains(text, "glab: HTTP 403") || !strings.Contains(text, "glab api --hostname") || !strings.Contains(text, "exit 1") {
		t.Fatalf("err = %s", text)
	}
}
