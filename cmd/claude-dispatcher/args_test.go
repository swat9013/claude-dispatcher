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
	for _, g := range globalFlags {
		for _, spelling := range g.spellings {
			if !strings.Contains(options, "\n  "+spelling) && !strings.Contains(options, ", "+spelling+" ") {
				t.Errorf("Options に %s が無い:%s", spelling, options)
			}
		}
	}
}

func TestEveryDeclaredGlobalFlagIsAcceptedInPlaceOfACommand(t *testing.T) {
	for _, g := range globalFlags {
		spellings := g.spellings
		if g.word != "" {
			spellings = append(spellings[:len(spellings):len(spellings)], g.word)
		}
		for _, spelling := range spellings {
			t.Run(spelling, func(t *testing.T) {
				var stdout, stderr bytes.Buffer

				code := run([]string{spelling}, &stdout, &stderr)

				if code != 0 || stdout.Len() == 0 {
					t.Fatalf("run(%s) = %d, stdout %q, stderr %q", spelling, code, stdout.String(), stderr.String())
				}
			})
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
	got, err := loopFlags([]string{"W.md", "--max-concurrent=2", "--dry-run"})

	if want := (loopOptions{path: "W.md", dryRun: true, maxConcurrent: 2}); err != nil || got != want {
		t.Fatalf("loopFlags = %+v, %v, want %+v", got, err, want)
	}
}
