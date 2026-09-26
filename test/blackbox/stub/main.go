// stub は black-box テストで PATH に置く外部 CLI (gh / claude / git / ps) の代役。
//
// 1 つの binary を名前ごとに hard link して使い、起動された名前で振る舞いを引く。置き場の root は
// env ではなく binary の隣の `.stub-root` から読む — `tick --dry-run --cron-env` は env を剥がして撃ち直すので、
// env で渡すと撃ち直し後の呼び出しが記録から漏れる。
//
// root の下:
//
//	calls/<name>/<unixnano>-<pid>.json  呼び出し 1 回の記録 (argv / cwd / 認証と置き場に関わる env)
//	responses/<name>.json               応答の rule 列。先頭から見て最初に当たった rule で応答する
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// recordedEnv は呼び出しの記録に残す env。token の受け渡しと置き場の解決を assert するためのもの
var recordedEnv = []string{
	"GH_TOKEN", "GITHUB_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN",
	"HOME", "PATH", "XDG_CONFIG_HOME", "XDG_STATE_HOME",
}

type call struct {
	Exe  string            `json:"exe"`
	Argv []string          `json:"argv"`
	Cwd  string            `json:"cwd"`
	Env  map[string]string `json:"env"`
}

type rule struct {
	// ArgsPrefix は argv[1:] の先頭一致。空なら何にでも当たる
	ArgsPrefix []string `json:"args_prefix,omitempty"`
	// ArgContains は argv[1:] のどれかが含む部分文字列。空なら条件にしない
	ArgContains string `json:"arg_contains,omitempty"`
	Stdout      string `json:"stdout,omitempty"`
	Stderr      string `json:"stderr,omitempty"`
	Exit        int    `json:"exit,omitempty"`
	SleepMillis int    `json:"sleep_ms,omitempty"`
	// Decisions は orchestrator の代役: state dir の最新の指示ファイルと同じ stem で決定ファイルを書く
	Decisions *decisionsWrite `json:"decisions,omitempty"`
}

type decisionsWrite struct {
	StateDir string `json:"state_dir"`
	Content  string `json:"content"`
}

func main() {
	name := filepath.Base(os.Args[0])
	root, err := stubRoot()
	if err != nil {
		fail(name, err)
	}
	if err := record(root, name); err != nil {
		fail(name, err)
	}
	r, err := matchRule(root, name, os.Args[1:])
	if err != nil {
		fail(name, err)
	}
	if r == nil {
		return
	}
	if r.Decisions != nil {
		if err := writeDecisions(*r.Decisions); err != nil {
			fail(name, err)
		}
	}
	fmt.Fprint(os.Stdout, r.Stdout)
	fmt.Fprint(os.Stderr, r.Stderr)
	time.Sleep(time.Duration(r.SleepMillis) * time.Millisecond)
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
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(exe), ".stub-root"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func record(root, name string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	env := map[string]string{}
	for _, key := range recordedEnv {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(call{Exe: exe, Argv: os.Args, Cwd: cwd, Env: env})
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "calls", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	file := filepath.Join(dir, fmt.Sprintf("%020d-%d.json", time.Now().UnixNano(), os.Getpid()))
	return os.WriteFile(file, raw, 0o644)
}

func matchRule(root, name string, args []string) (*rule, error) {
	raw, err := os.ReadFile(filepath.Join(root, "responses", name+".json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rules []rule
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

func matches(r rule, args []string) bool {
	if len(args) < len(r.ArgsPrefix) || !slices.Equal(args[:len(r.ArgsPrefix)], r.ArgsPrefix) {
		return false
	}
	if r.ArgContains == "" {
		return true
	}
	return slices.ContainsFunc(args, func(arg string) bool { return strings.Contains(arg, r.ArgContains) })
}

func writeDecisions(w decisionsWrite) error {
	instructions, err := filepath.Glob(filepath.Join(w.StateDir, "instructions", "*.json"))
	if err != nil {
		return err
	}
	if len(instructions) == 0 {
		return fmt.Errorf("指示ファイルが無い (%s/instructions)", w.StateDir)
	}
	sort.Strings(instructions)
	stem := strings.TrimSuffix(filepath.Base(instructions[len(instructions)-1]), ".json")
	dir := filepath.Join(w.StateDir, "decisions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, stem+".json"), []byte(w.Content), 0o644)
}
