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
	wants := map[string]string{"--help": "Usage: claude-dispatcher ", "--version": "claude-dispatcher "}
	for _, g := range globalFlags {
		want := wants[g.spellings[len(g.spellings)-1]]
		for _, spelling := range g.allSpellings() {
			t.Run(spelling, func(t *testing.T) {
				var stdout, stderr bytes.Buffer

				code := run([]string{spelling}, &stdout, &stderr)

				if code != 0 || want == "" || !strings.HasPrefix(stdout.String(), want) {
					t.Fatalf("run(%s) = %d, stdout %q, want 先頭 %q", spelling, code, stdout.String(), want)
				}
			})
		}
	}
}

// globalFlagsWhere は afterCommand が want の globalFlag の綴り。0 件なら test の前提が崩れているので落とす。
func globalFlagsWhere(t *testing.T, want bool) []string {
	t.Helper()
	var spellings []string
	for _, g := range globalFlags {
		if g.afterCommand == want {
			spellings = append(spellings, g.spellings...)
		}
	}
	if len(spellings) == 0 {
		t.Fatalf("afterCommand が %v の globalFlag が無い", want)
	}
	return spellings
}

func TestGlobalFlagDeclaredAfterCommandActsAfterACommandAsInPlaceOfIt(t *testing.T) {
	// Options 節が示す位置 (afterCommand) と振る舞いが同じ宣言から決まる
	for _, spelling := range globalFlagsWhere(t, true) {
		t.Run(spelling, func(t *testing.T) {
			var want, wantErr bytes.Buffer
			if code := run([]string{spelling}, &want, &wantErr); code != 0 {
				t.Fatalf("command の代わりの %s の exit = %d", spelling, code)
			}
			var stdout, stderr bytes.Buffer

			code := run([]string{"status", "--no-such-flag", spelling}, &stdout, &stderr)

			if code != 0 || stdout.String() != want.String() || stderr.Len() != 0 {
				t.Fatalf("command の後ろの %s が command の代わりと同じに振る舞わない: exit %d, stderr %q", spelling, code, stderr.String())
			}
		})
	}
}

func TestGlobalFlagNotDeclaredAfterCommandIsAnArgumentErrorAfterACommand(t *testing.T) {
	for _, spelling := range globalFlagsWhere(t, false) {
		t.Run(spelling, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			code := run([]string{"status", spelling}, &stdout, &stderr)

			if code != exitUsage || stdout.Len() != 0 {
				t.Fatalf("command の後ろに書けない %s を受けた: exit %d, stdout %q", spelling, code, stdout.String())
			}
		})
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
