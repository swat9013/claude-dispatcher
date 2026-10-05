package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/printable"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// settingsEntries は、worker が tracker を操作するのに要りそうな Claude Code の settings の permissions.allow の entry。
// tracker の CLI の綴りで書く
var settingsEntries = map[workflow.TrackerKind][]string{
	workflow.GitHub: {"Bash(gh issue:*)", "Bash(gh pr:*)", "Bash(git push:*)"},
	workflow.GitLab: {"Bash(glab issue:*)", "Bash(glab mr:*)", "Bash(git push:*)"},
}

// runDoctor は `doctor [<workflow の path>]` を撃つ (formats.md §7.4)。何も書かない。確かめたことを 1 件 1 行で出し、
// NG があれば exit 1 (警告では落とさない)。
func runDoctor(args []string, stdout, stderr io.Writer) int {
	path, ok := workflowArg(args, nil, stderr)
	if !ok {
		return exitUsage
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		fmt.Fprintf(stderr, "workflow 定義の path を解決できない (%s): %v\n", path, err)
		return exitFailed
	}
	e := newEnvironment()
	def, err := workflow.Load(abs, e.getenv)
	if err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			fmt.Fprintf(stdout, "NG   %s\n", line)
		}
		return exitFailed
	}
	fmt.Fprintf(stdout, "ok   workflow 定義 %s\n", abs)
	failed := false
	for _, check := range []func(environment, workflow.Definition, io.Writer) bool{checkStore, checkCommands, checkPrecheck} {
		if !check(e, def, stdout) {
			failed = true
		}
	}
	for _, w := range promiseWarnings(def) {
		fmt.Fprintf(stdout, "警告 %s\n", w)
	}
	fmt.Fprintln(stdout, "settings の permissions.allow に要りそうな entry (CLI は書かない):")
	for _, entry := range settingsEntries[def.Tracker.Kind] {
		fmt.Fprintf(stdout, "  %s\n", entry)
	}
	if failed {
		return exitFailed
	}
	return 0
}

// checkStore は issue 置き場を読めるかを確かめる。
func checkStore(e environment, def workflow.Definition, stdout io.Writer) bool {
	if _, err := loop.OpenItems(e.store(def), def); err != nil {
		fmt.Fprintf(stdout, "NG   issue 置き場 %s: %s\n", def.Tracker.Place(), printable.Line(err.Error()))
		return false
	}
	fmt.Fprintf(stdout, "ok   issue 置き場 %s\n", def.Tracker.Place())
	return true
}

// checkCommands は、loop の起動時と同じく依存 CLI を解決できるかを確かめる (formats.md §6)。
func checkCommands(e environment, def workflow.Definition, stdout io.Writer) bool {
	ok := true
	for _, name := range requiredCommands(def) {
		if path, err := deps.Lookup(name, e.env); err != nil {
			fmt.Fprintf(stdout, "NG   %s\n", printable.Line(err.Error()))
			ok = false
		} else {
			fmt.Fprintf(stdout, "ok   %s %s\n", name, path)
		}
	}
	return ok
}

// checkPrecheck は事前検査 (formats.md §2.9) を通す。
func checkPrecheck(e environment, def workflow.Definition, stdout io.Writer) bool {
	problems := e.precheck(def)
	for _, p := range problems {
		fmt.Fprintf(stdout, "NG   %s\n", p)
	}
	if len(problems) > 0 {
		return false
	}
	fmt.Fprintln(stdout, "ok   事前検査")
	return true
}

// promiseWarnings は、利用者の約束に頼る CL 側の trigger の宣言 (system.md §11)。
func promiseWarnings(def workflow.Definition) []string {
	var warnings []string
	for _, t := range def.Triggers {
		if t.On != target.KindCL {
			continue
		}
		p := t.CL
		if p.Approved != nil && *p.Approved {
			warnings = append(warnings, fmt.Sprintf("trigger %s: approved: true の CL に action を当てている (merge を worker に任せうる)", t.Name))
		}
		if p.Head == "" && len(p.LabelsAll) == 0 && len(p.LabelsAny) == 0 && (p.SameRepo == nil || !*p.SameRepo) {
			warnings = append(warnings, fmt.Sprintf("trigger %s: head・labels.all・labels.any・same_repo: true のどれでも絞っていない (人の CL や fork の CL に worker を送りうる)", t.Name))
		}
	}
	return warnings
}
