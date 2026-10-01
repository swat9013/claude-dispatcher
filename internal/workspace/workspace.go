// Package workspace は作業対象ごとの workspace を作り、hooks を撃ち、消す (system.md §7、formats.md §2.5)。
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

// Path は issue の workspace の path。root の外へは出ない (番号だけから作る)。
func (m Manager) Path(number int) string {
	return filepath.Join(m.Root, target.FileName(number))
}

// Prepare は workspace を用意する。無ければ作って after_create を撃ち (失敗したら作りかけを消す)、before_run を撃つ。
func (m Manager) Prepare(number int) (string, error) {
	path := m.Path(number)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return "", fmt.Errorf("workspace を作れない (%s): %w", path, err)
		}
		if err := m.run("after_create", m.Hooks.AfterCreate, number); err != nil {
			if rmErr := os.RemoveAll(path); rmErr != nil {
				return "", fmt.Errorf("%w (作りかけの workspace も消せない: %v)", err, rmErr)
			}
			return "", err
		}
	} else if err != nil {
		return "", fmt.Errorf("workspace を読めない (%s): %w", path, err)
	}
	if err := m.run("before_run", m.Hooks.BeforeRun, number); err != nil {
		return "", err
	}
	return path, nil
}

// AfterRun は after_run を撃つ。
func (m Manager) AfterRun(number int) error { return m.run("after_run", m.Hooks.AfterRun, number) }

// Remove は before_remove を撃ってから workspace を消す。before_remove が失敗しても消す。返す error はどちらの失敗も含む。
func (m Manager) Remove(number int) error {
	hookErr := m.run("before_remove", m.Hooks.BeforeRemove, number)
	if err := os.RemoveAll(m.Path(number)); err != nil {
		return errors.Join(hookErr, fmt.Errorf("workspace を消せない (%s): %w", m.Path(number), err))
	}
	return hookErr
}

// Existing は root の下にある issue の workspace の番号を返す。root が無ければ空。番号として読めない dir は飛ばす。
func (m Manager) Existing() ([]int, error) {
	entries, err := os.ReadDir(m.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workspace root を読めない (%s): %w", m.Root, err)
	}
	var numbers []int
	for _, e := range entries {
		if n, ok := target.NumberOf(e.Name()); ok && e.IsDir() {
			numbers = append(numbers, n)
		}
	}
	return numbers, nil
}

// run は hook の script を workspace を cwd にして `sh -c` で撃つ。空なら撃たない。
func (m Manager) run(name, script string, number int) error {
	if script == "" {
		return nil
	}
	path := m.Path(number)
	env := append(append([]string{}, m.Env...),
		"CLAUDE_DISPATCHER_WORKSPACE="+path,
		"CLAUDE_DISPATCHER_CLONE="+m.Clone,
		"CLAUDE_DISPATCHER_KIND=issue",
		"CLAUDE_DISPATCHER_NUMBER="+strconv.Itoa(number),
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
