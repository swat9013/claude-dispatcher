package worker

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/workspace"
)

// worker log の lock を別の process が持っている作業対象 (system.md §7 の orphan)。

func TestAttemptWhoseWorkerLogIsLockedFailsWithoutRunningHooks(t *testing.T) {
	dir := t.TempDir()
	marks := filepath.Join(dir, "marks")
	ref := target.Ref{Kind: target.KindIssue, Number: 1}
	log := LogFile(filepath.Join(dir, "state"), ref)
	if err := os.MkdirAll(filepath.Dir(log), 0o755); err != nil {
		t.Fatal(err)
	}
	holder, err := os.Create(log)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	hook := `: > "` + marks + `-$CLAUDE_DISPATCHER_NUMBER"`
	r := Runner{StateDir: filepath.Join(dir, "state"), Workspaces: workspace.Manager{
		Root: filepath.Join(dir, "workspaces"), HookLock: &sync.Mutex{},
		Hooks: workspace.Hooks{AfterCreate: hook, BeforeRun: hook, AfterRun: hook, Timeout: time.Minute},
	}}

	result := r.attempt(Job{Item: target.Issue{Number: 1}}, &Run{stop: make(chan struct{})}, notices{})

	if !strings.Contains(result.Failure, "別の process が持っている") {
		t.Errorf("Failure = %q, want lock を別の process が持っていること", result.Failure)
	}
	if _, err := os.Stat(marks + "-1"); !os.IsNotExist(err) {
		t.Errorf("hooks を撃った: %v", err)
	}
}
