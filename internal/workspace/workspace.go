// Package workspace は作業対象ごとの workspace を作り、hooks を撃ち、消す (system.md §7、formats.md §2.6)。
// hooks は本物の shell で撃つ (seam を置かない — system.md §13)。
package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/proc"
	"github.com/swat9013/claude-dispatcher/internal/target"
)

// Hooks は workspace の hooks の shell script。空の hook は撃たない。
type Hooks struct {
	AfterCreate  string
	BeforeRun    string
	AfterRun     string
	BeforeRemove string
	Timeout      time.Duration
}

// Manager は 1 つの root の下の workspace を扱う。
type Manager struct {
	// Root は workspace を置く dir の絶対 path
	Root string
	// Clone は workflow 定義の dir の絶対 path。hooks に渡す
	Clone string
	Hooks Hooks
	// Env は hooks に渡す環境 (loop の環境)
	Env []string
}

// Path は作業対象の workspace の path。root の外へは出ない (種類と番号だけから作る)。
func (m Manager) Path(ref target.Ref) string {
	return filepath.Join(m.Root, ref.FileName())
}

// Prepare は workspace を用意する。無ければ作って after_create を撃ち (失敗したら作りかけを消す)、before_run を撃つ。
func (m Manager) Prepare(ref target.Ref) (string, error) {
	path := m.Path(ref)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return "", fmt.Errorf("workspace を作れない (%s): %w", path, err)
		}
		if err := m.run("after_create", m.Hooks.AfterCreate, ref); err != nil {
			if rmErr := os.RemoveAll(path); rmErr != nil {
				return "", fmt.Errorf("%w (作りかけの workspace も消せない: %v)", err, rmErr)
			}
			return "", err
		}
	} else if err != nil {
		return "", fmt.Errorf("workspace を読めない (%s): %w", path, err)
	}
	if err := m.run("before_run", m.Hooks.BeforeRun, ref); err != nil {
		return "", err
	}
	return path, nil
}

// AfterRun は after_run を撃つ。
func (m Manager) AfterRun(ref target.Ref) error { return m.run("after_run", m.Hooks.AfterRun, ref) }

// Remove は before_remove を撃ってから workspace を消す。before_remove が失敗しても消す。返す error はどちらの失敗も含む。
func (m Manager) Remove(ref target.Ref) error {
	hookErr := m.run("before_remove", m.Hooks.BeforeRemove, ref)
	if err := os.RemoveAll(m.Path(ref)); err != nil {
		return errors.Join(hookErr, fmt.Errorf("workspace を消せない (%s): %w", m.Path(ref), err))
	}
	return hookErr
}

// Existing は root の下にある workspace の作業対象を返す。root が無ければ空。作業対象の名前として読めない dir は飛ばす。
func (m Manager) Existing() ([]target.Ref, error) {
	entries, err := os.ReadDir(m.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workspace root を読めない (%s): %w", m.Root, err)
	}
	var refs []target.Ref
	for _, e := range entries {
		if ref, ok := target.ParseFileName(e.Name()); ok && e.IsDir() {
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

// branchTimeout は workspace の branch を読む git 1 回の上限
const branchTimeout = 10 * time.Second

// Branch は作業対象の workspace で checkout されている branch の名前を返す。workspace が無い・git の作業ツリーでない・
// detached HEAD なら "" を返す。git は Env の PATH から探す。
func (m Manager) Branch(ref target.Ref) (string, error) {
	path := m.Path(ref)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	git, err := deps.Lookup("git", m.Env)
	if err != nil {
		return "", err
	}
	out, err := proc.Command{Path: git, Env: m.Env, Dir: path, Timeout: branchTimeout}.Output("symbolic-ref", "--short", "-q", "HEAD")
	var failed *proc.Error
	switch {
	case errors.As(err, &failed) && failed.Exit == 1:
		// -q の symbolic-ref は、HEAD が branch を指していない (detached) と何も出さずに 1 で終わる
		return "", nil
	case errors.As(err, &failed) && strings.Contains(failed.Stderr, "not a git repository"):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("workspace の branch を読めない (%s): %w", path, err)
	}
	return strings.TrimSpace(out), nil
}

// run は hook の script を workspace を cwd にして `sh -c` で撃つ。空なら撃たない。
func (m Manager) run(name, script string, ref target.Ref) error {
	if script == "" {
		return nil
	}
	path := m.Path(ref)
	env := append(append([]string{}, m.Env...),
		"CLAUDE_DISPATCHER_WORKSPACE="+path,
		"CLAUDE_DISPATCHER_CLONE="+m.Clone,
		"CLAUDE_DISPATCHER_KIND="+string(ref.Kind),
		"CLAUDE_DISPATCHER_NUMBER="+strconv.Itoa(ref.Number),
	)
	_, err := proc.Command{Path: "/bin/sh", Env: env, Dir: path, Timeout: m.Hooks.Timeout}.Output("-c", script)
	var failed *proc.Error
	if errors.As(err, &failed) {
		return fmt.Errorf("%s の失敗 (exit %d): %s", name, failed.Exit, strings.TrimSpace(failed.Stderr))
	}
	if err != nil {
		return fmt.Errorf("%s の失敗: %w", name, err)
	}
	return nil
}
