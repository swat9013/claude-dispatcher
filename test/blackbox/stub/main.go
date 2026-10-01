// stub は black-box テストで PATH に置く外部 CLI (gh / claude / git) の代役。
//
// 1 つの binary を名前ごとに hard link して使い、起動された名前で振る舞いを引く。harness との取り決め
// (置き場と JSON の形) は stubwire が持つ。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	if err := record(root, name); err != nil {
		fail(name, err)
	}
	r, err := matchRule(root, name, os.Args[1:])
	if err != nil {
		fail(name, err)
	}
	if r == nil {
		fmt.Fprintf(os.Stderr, "stub %s: 応答 rule の無い呼び出し: %q\n", name, os.Args[1:])
		os.Exit(stubwire.UnmatchedExit)
	}
	for _, w := range r.Writes {
		if err := writeAtomically(w.Path, []byte(w.Content)); err != nil {
			fail(name, err)
		}
	}
	fmt.Fprint(os.Stdout, r.Stdout)
	fmt.Fprint(os.Stderr, r.Stderr)
	if r.ReleaseFile != "" && !waitForRelease(r.ReleaseFile) {
		fmt.Fprintf(os.Stderr, "stub %s: %s が %s 経っても現れない\n", name, r.ReleaseFile, stubwire.ReleaseDeadline)
		os.Exit(stubwire.ReleaseTimeoutExit)
	}
	os.Exit(r.Exit)
}

// writeAtomically は読み手に書きかけを見せないよう、同じ dir の一時 file に書いてから path へ rename する。
// 一時 file の名前は末尾が .tmp で、path の拡張子で glob する読み手には当たらない。
func writeAtomically(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// waitForRelease は file が現れるまで待つ。上限を過ぎたら false。
func waitForRelease(file string) bool {
	deadline := time.Now().Add(stubwire.ReleaseDeadline)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(file); err == nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
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

func record(root, name string) error {
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
	raw, err := json.Marshal(stubwire.Call{Exe: exe, Argv: os.Args, Cwd: cwd, Env: env})
	if err != nil {
		return err
	}
	// harness は呼び出しを待つ間 *.json を読み続けるので、writeAtomically で書きかけを見せない。
	file := filepath.Join(stubwire.CallsDir(root, name), fmt.Sprintf("%020d-%d.json", time.Now().UnixNano(), os.Getpid()))
	return writeAtomically(file, raw)
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
	for _, part := range r.ArgsContain {
		if !slices.ContainsFunc(args, func(arg string) bool { return strings.Contains(arg, part) }) {
			return false
		}
	}
	return true
}
