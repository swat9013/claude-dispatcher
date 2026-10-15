package main

import (
	"bytes"
	"strings"
	"testing"
)

// optionsSection は `--help` の出力の Options 節の行。
func optionsSection(t *testing.T) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("--help の exit = %d", code)
	}
	_, after, ok := strings.Cut(stdout.String(), "\nOptions:\n")
	if !ok {
		t.Fatalf("Options 節が無い:\n%s", stdout.String())
	}
	section, _, _ := strings.Cut(after, "\n\n")
	return section
}

func TestUsageListsEveryDeclaredFlagUnderOptions(t *testing.T) {
	options := "\n" + optionsSection(t)

	for _, c := range commandSpecs {
		for _, f := range c.flags {
			_, line, _ := strings.Cut(options, "\n  "+f.name+" ")
			line, _, _ = strings.Cut(line, "\n")
			if !strings.Contains(line, "("+c.name+") ") {
				t.Errorf("Options に %s の %s が無い:%s", c.name, f.name, options)
			}
		}
	}
	for _, f := range globalFlags {
		if !strings.Contains(options, "\n  "+f.name+" ") {
			t.Errorf("Options に %s が無い:%s", f.name, options)
		}
	}
}

func TestEveryDeclaredValueFlagRejectsAFlagSpellingAsItsValue(t *testing.T) {
	for _, c := range commandSpecs {
		for _, f := range c.flags {
			if !f.takesValue() {
				continue
			}
			t.Run(c.name+" "+f.name, func(t *testing.T) {
				_, err := c.parse([]string{f.name, "--dry-run"})

				if err == nil || err.Error() != f.name+" に値が要る" {
					t.Fatalf("err = %v, want %s に値が要る", err, f.name)
				}
			})
		}
	}
}

func TestEveryCommandRejectsAnUnknownFlag(t *testing.T) {
	for _, c := range commandSpecs {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.parse([]string{"--no-such-flag"})

			if err == nil {
				t.Fatalf("%s が未知の flag を受けた", c.name)
			}
		})
	}
}

func TestPositionalArgumentAndFlagsMayBeMixed(t *testing.T) {
	got, err := commandSpecOf("loop").parse([]string{"W.md", "--max-concurrent=2", "--dry-run"})

	if err != nil || got.workflowPath != "W.md" || got.value("--max-concurrent") != "2" || !got.has("--dry-run") {
		t.Fatalf("parse = %+v, %v", got, err)
	}
}
