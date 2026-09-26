// stub は black-box テストで PATH に置く外部 CLI (gh / claude / git / ps / crontab) の代役。
//
// 1 つの binary を名前ごとに hard link して使い、起動された名前で振る舞いを引く。harness との取り決め
// (置き場と JSON の形) は stubwire が持つ。crontab だけは rule でなく、表を stub root に持つ fake として振る舞う。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/swat9013/claude-dispatcher/test/blackbox/stubwire"
)

func main() {
	name := filepath.Base(os.Args[0])
	root, err := stubRoot()
	if err != nil {
		fail(name, err)
	}
	var stdin string
	if name == "crontab" && slices.Equal(os.Args[1:], []string{"-"}) {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			fail(name, err)
		}
		stdin = string(raw)
	}
	if err := record(root, name, stdin); err != nil {
		fail(name, err)
	}
	if name == "crontab" {
		os.Exit(crontab(root, os.Args[1:], stdin))
	}
	r, err := matchRule(root, name, os.Args[1:])
	if err != nil {
		fail(name, err)
	}
	if r == nil {
		fmt.Fprintf(os.Stderr, "stub %s: 応答 rule の無い呼び出し: %q\n", name, os.Args[1:])
		os.Exit(stubwire.UnmatchedExit)
	}
	if r.Decisions != nil {
		if err := writeDecisions(*r.Decisions); err != nil {
			fail(name, err)
		}
	}
	fmt.Fprint(os.Stdout, r.Stdout)
	fmt.Fprint(os.Stderr, r.Stderr)
	os.Exit(r.Exit)
}

func fail(name string, err error) {
	fmt.Fprintf(os.Stderr, "stub %s: %v\n", name, err)
	os.Exit(97)
}

func stubRoot() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(exe), stubwire.RootFile))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// crontab は `crontab -l` (表を出す。無ければ exit 1) と `crontab -` (stdin の表で置き換える) だけを受ける fake。
func crontab(root string, args []string, stdin string) int {
	file := stubwire.CrontabFile(root)
	switch {
	case slices.Equal(args, []string{"-l"}):
		raw, err := os.ReadFile(file)
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "crontab: no crontab for stub")
			return 1
		}
		if err != nil {
			fail("crontab", err)
		}
		fmt.Print(string(raw))
		return 0
	case slices.Equal(args, []string{"-"}):
		if _, err := os.Stat(stubwire.CrontabRefuseFile(root)); err == nil {
			fmt.Fprintln(os.Stderr, "crontab: stub refuses to install the table")
			return 1
		}
		if err := os.WriteFile(file, []byte(stdin), 0o644); err != nil {
			fail("crontab", err)
		}
		return 0
	}
	fmt.Fprintf(os.Stderr, "stub crontab: 受けない呼び出し: %q\n", args)
	return stubwire.UnmatchedExit
}

func record(root, name, stdin string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	env := map[string]string{}
	for _, key := range stubwire.RecordedEnv {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	raw, err := json.Marshal(stubwire.Call{Exe: exe, Argv: os.Args, Cwd: cwd, Env: env, Stdin: stdin})
	if err != nil {
		return err
	}
	dir := stubwire.CallsDir(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	file := filepath.Join(dir, fmt.Sprintf("%020d-%d.json", time.Now().UnixNano(), os.Getpid()))
	return os.WriteFile(file, raw, 0o644)
}

func matchRule(root, name string, args []string) (*stubwire.Rule, error) {
	raw, err := os.ReadFile(stubwire.ResponsesFile(root, name))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rules []stubwire.Rule
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, err
	}
	for i := range rules {
		if matches(rules[i], args) {
			return &rules[i], nil
		}
	}
	return nil, nil
}

func matches(r stubwire.Rule, args []string) bool {
	if len(args) < len(r.ArgsPrefix) || !slices.Equal(args[:len(r.ArgsPrefix)], r.ArgsPrefix) {
		return false
	}
	if r.ArgContains == "" {
		return true
	}
	return slices.ContainsFunc(args, func(arg string) bool { return strings.Contains(arg, r.ArgContains) })
}

func writeDecisions(w stubwire.DecisionsWrite) error {
	instructions, err := filepath.Glob(stubwire.InstructionsGlob(w.StateDir))
	if err != nil {
		return err
	}
	if len(instructions) == 0 {
		return fmt.Errorf("指示ファイルが無い (%s)", stubwire.InstructionsGlob(w.StateDir))
	}
	sort.Strings(instructions)
	stem := strings.TrimSuffix(filepath.Base(instructions[len(instructions)-1]), ".json")
	file := stubwire.DecisionsFile(w.StateDir, stem)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	for _, issue := range w.ObstructWorkerLogs {
		if err := os.MkdirAll(stubwire.WorkerLogFile(w.StateDir, issue, stem), 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(file, []byte(w.Content), 0o644)
}
