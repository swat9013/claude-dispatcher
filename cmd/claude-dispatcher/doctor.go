package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/places"
	"github.com/swat9013/claude-dispatcher/internal/printable"
	"github.com/swat9013/claude-dispatcher/internal/provider"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// runDoctor は `doctor [<workflow の path>]` を撃つ (formats.md §7.4)。何も書かない。確かめたことを 1 件 1 行で出し、
// NG があれば exit 1 (警告では落とさない)。
func runDoctor(args []string, stdout, stderr io.Writer) int {
	path, ok := workflowArg("doctor", args, stderr)
	if !ok {
		return exitUsage
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		fmt.Fprintf(stderr, "workflow 定義の path を解決できない (%s): %v\n", path, err)
		return exitFailed
	}
	def, err := workflow.Load(abs, unresolvedEnvironment().getenv)
	if err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			fmt.Fprintf(stdout, "NG   %s\n", line)
		}
		fmt.Fprintln(stdout, guidePointer)
		return exitFailed
	}
	// loop の起動時と同じく、workflow 定義が撃つ依存 CLI で PATH を解決する
	e := definitionEnvironment(def)
	fmt.Fprintf(stdout, "ok   workflow 定義 %s\n", abs)
	failed := !checkStore(e, def, stdout, stderr)
	for _, check := range []func(environment, workflow.Definition, io.Writer) bool{checkCLPlace, checkCommands, checkPrecheck} {
		if !check(e, def, stdout) {
			failed = true
		}
	}
	for _, w := range promiseWarnings(def) {
		fmt.Fprintf(stdout, "警告 %s\n", w)
	}
	// worker が外部 store を操作するのに要りそうな entry を、役割ごとに、その役割を担う provider の CLI の綴りで出す
	// (system.md §11)
	fmt.Fprintln(stdout, "settings の permissions.allow に要りそうな entry (CLI は書かない):")
	for _, entry := range def.Settings() {
		fmt.Fprintf(stdout, "  %s\n", entry)
	}
	if failed {
		return exitFailed
	}
	return 0
}

// checkStore は issue 置き場を読めるかを確かめる。確認の手順を持つ種類は loop の起動時と同じ確認 (formats.md §6) を通し、
// workflow 定義が書いた名前のうち実在しないものを 1 つ 1 行で名指しする。cl: の CL 置き場は checkCLPlace が確かめる。
// 一覧の読み出しが遅ければ、読み終わる前に stderr に 1 行出す (formats.md §7.4)。
func checkStore(e environment, def workflow.Definition, stdout, stderr io.Writer) bool {
	if err := e.places(def).ConfirmIssues(); err != nil {
		var unknown *target.UnknownNames
		if errors.As(err, &unknown) {
			for _, p := range unknown.Problems {
				fmt.Fprintf(stdout, "NG   %s\n", printable.Line(p))
			}
			return false
		}
		fmt.Fprintf(stdout, "NG   issue 置き場 %s: %s\n", def.Tracker.Place, printable.Line(err.Error()))
		return false
	}
	store := e.places(def).Store()
	store.Slow = slowToStderr(def, stderr)
	var err error
	if _, _, separate := def.SeparateCLHost(); separate {
		// CL は別の置き場にあり、checkCLPlace の行で確かめる
		_, err = store.Open(target.KindIssue)
	} else {
		_, err = loop.OpenItems(store, def)
	}
	return placeLine(stdout, "issue 置き場", def.Tracker.Place, err)
}

// checkCLPlace は cl: の CL 置き場を、loop の起動時と同じ確認 (formats.md §6) で読めるかを確かめる。cl: が無ければ何も出さない。
func checkCLPlace(e environment, def workflow.Definition, stdout io.Writer) bool {
	_, place, ok := def.SeparateCLHost()
	if !ok {
		return true
	}
	return placeLine(stdout, "CL 置き場", place, e.places(def).ConfirmCLs())
}

// placeLine は置き場を読んだ結果 err を、name の置き場の 1 行に出す。処理を続けられる失敗 (*target.NonFatal) は、置き場を
// 読めているので NG にせず、ok の行に理由を添える (formats.md §7.4)。
func placeLine(stdout io.Writer, name string, place provider.Place, err error) bool {
	var nonFatal *target.NonFatal
	switch {
	case errors.As(err, &nonFatal):
		fmt.Fprintf(stdout, "ok   %s %s (補助の要求が落ちた: %s)\n", name, place, printable.Line(nonFatal.Err.Error()))
	case err != nil:
		fmt.Fprintf(stdout, "NG   %s %s: %s\n", name, place, printable.Line(err.Error()))
		return false
	default:
		fmt.Fprintf(stdout, "ok   %s %s\n", name, place)
	}
	return true
}

// checkCommands は、loop の起動時と同じく依存 CLI を解決できるかを確かめる (formats.md §6)。
func checkCommands(e environment, def workflow.Definition, stdout io.Writer) bool {
	ok := true
	for _, name := range places.RequiredCommands(def) {
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
