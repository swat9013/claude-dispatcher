package workspace_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/workspace"
)

// hooks を作業対象を跨いで直列に撃つか並列に撃つか (formats.md §2.1 の hooks.*、system.md §7 の workspace)。本物の sh を撃つ。

var (
	issue1 = target.Ref{Kind: target.KindIssue, Number: 1}
	issue2 = target.Ref{Kind: target.KindIssue, Number: 2}
)

// overlapDetector は、issue1 と issue2 の hook が重なって走ると重なりの印を残して exit 1 で落ちる script を返す。
// 始めに in-<番号>、終わりに out-<番号> の印を置き、その間に相手の in を最長 2s 待つ。相手の in があって out が
// まだ無ければ重なっている。重なると、少なくとも片方は相手の out より先に相手の in を見るので、窓の長さに依らずに検出する。
func overlapDetector(dir string) string {
	return `n=$CLAUDE_DISPATCHER_NUMBER; o=$((3 - n)); d='` + dir + `'
touch "$d/in-$n"
i=0
while [ ! -e "$d/in-$o" ] && [ "$i" -lt 20 ]; do sleep 0.1; i=$((i + 1)); done
if [ -e "$d/in-$o" ] && [ ! -e "$d/out-$o" ]; then touch "$d/overlap"; exit 1; fi
touch "$d/out-$n"`
}

// rendezvous は、issue1 と issue2 の hook が互いに相手の到着を待つ script を返す。相手が 5s 来なければ exit 1 で落ちる。
// 直列に撃たれると、先の hook は相手を待ちきれずに落ちる。
func rendezvous(dir string) string {
	return `touch '` + dir + `/'"$CLAUDE_DISPATCHER_NUMBER"; other=$((3 - CLAUDE_DISPATCHER_NUMBER)); i=0; ` +
		`while [ ! -e '` + dir + `/'"$other" ]; do i=$((i + 1)); [ "$i" -gt 50 ] && exit 1; sleep 0.1; done`
}

// loopManager は、loop が作るのと同じく、hooks の直列の lock を 1 つ持つ Manager を返す。worker と loop はそれぞれ
// この値の写しを使う。
func loopManager(t *testing.T, hooks workspace.Hooks) workspace.Manager {
	t.Helper()
	hooks.Timeout = 10 * time.Second
	return workspace.Manager{Root: t.TempDir(), Env: []string{"PATH=" + os.Getenv("PATH")}, Hooks: hooks, HookLock: &sync.Mutex{}}
}

// together は 2 つの操作を同時に撃ち、両方の error を返す。
func together(a, b func() error) (error, error) {
	var errA, errB error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); errA = a() }()
	go func() { defer wg.Done(); errB = b() }()
	wg.Wait()
	return errA, errB
}

func prepare(m workspace.Manager, ref target.Ref) func() error {
	return func() error { _, err := m.Prepare(ref); return err }
}

func assertNoOverlap(t *testing.T, dir string, errA, errB error) {
	t.Helper()
	if errA != nil || errB != nil {
		t.Errorf("errors = %v / %v", errA, errB)
	}
	if _, err := os.Stat(filepath.Join(dir, "overlap")); err == nil {
		t.Error("hook が重なって走った")
	}
}

func TestAfterCreateOfTwoWorkspacesPreparedAtOnceDoesNotOverlap(t *testing.T) {
	dir := t.TempDir()
	m := loopManager(t, workspace.Hooks{AfterCreate: overlapDetector(dir)})
	worker1, worker2 := m, m

	err1, err2 := together(prepare(worker1, issue1), prepare(worker2, issue2))

	assertNoOverlap(t, dir, err1, err2)
}

func TestAfterCreateDoesNotOverlapBeforeRemoveOfAnotherWorkspace(t *testing.T) {
	dir := t.TempDir()
	m := loopManager(t, workspace.Hooks{AfterCreate: overlapDetector(dir), BeforeRemove: overlapDetector(dir)})
	if err := os.MkdirAll(m.Path(issue2), 0o755); err != nil {
		t.Fatal(err)
	}
	worker, loop := m, m

	errPrepare, errRemove := together(prepare(worker, issue1), func() error { return loop.Remove(issue2) })

	assertNoOverlap(t, dir, errPrepare, errRemove)
}

func TestRemovingWithoutABeforeRemoveHookDoesNotWaitForAnotherAfterCreate(t *testing.T) {
	m := loopManager(t, workspace.Hooks{AfterCreate: "exit 0"})
	if err := os.MkdirAll(m.Path(issue2), 0o755); err != nil {
		t.Fatal(err)
	}
	// 他の workspace の after_create が走っている間
	m.HookLock.Lock()
	defer m.HookLock.Unlock()
	removed := make(chan error, 1)

	go func() { removed <- m.Remove(issue2) }()

	select {
	case err := <-removed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("撃つ hook が無いのに、他の after_create の終わりを待った")
	}
}

func TestBeforeRunOfTwoWorkspacesCanRunAtOnce(t *testing.T) {
	dir := t.TempDir()
	m := loopManager(t, workspace.Hooks{BeforeRun: rendezvous(dir)})

	err1, err2 := together(prepare(m, issue1), prepare(m, issue2))

	if err1 != nil || err2 != nil {
		t.Errorf("before_run が並列に走らなかった: %v / %v", err1, err2)
	}
}

func TestAfterRunOfTwoWorkspacesCanRunAtOnce(t *testing.T) {
	dir := t.TempDir()
	m := loopManager(t, workspace.Hooks{AfterRun: rendezvous(dir)})
	for _, ref := range []target.Ref{issue1, issue2} {
		if _, err := m.Prepare(ref); err != nil {
			t.Fatal(err)
		}
	}

	err1, err2 := together(func() error { return m.AfterRun(issue1) }, func() error { return m.AfterRun(issue2) })

	if err1 != nil || err2 != nil {
		t.Errorf("after_run が並列に走らなかった: %v / %v", err1, err2)
	}
}
