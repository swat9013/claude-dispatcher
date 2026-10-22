package coord

import (
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/proc"
	"github.com/swat9013/claude-dispatcher/internal/target"
)

// 実物の git で、一時 dir の bare repo を調停 remote にして撃つ (formats.md §12 の git の撃ち方)。

// remoteForTest は bare repo を origin に持つ clone を作り、その clone から撃つ Remote を返す。
func remoteForTest(t *testing.T) Remote {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git が無い")
	}
	root := t.TempDir()
	bare, clone := filepath.Join(root, "remote.git"), filepath.Join(root, "clone")
	for _, args := range [][]string{{"init", "-q", "--bare", bare}, {"init", "-q", clone}, {"-C", clone, "remote", "add", "origin", bare}} {
		if out, err := exec.Command(git, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// 利用者の git の設定に頼らないことを確かめるため、HOME と global の設定を空にする
	env := []string{"HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	return Remote{Name: "origin", ScopeDir: "github.com_acme_widgets-1a2b3c4d", Git: proc.Command{Path: git, Dir: clone, Env: env, Timeout: 30 * time.Second}}
}

func TestClaimIsCreatedOnlyWhenAbsent(t *testing.T) {
	r := remoteForTest(t)
	ref := r.ClaimRef(target.Ref{Kind: target.KindIssue, Number: 42})
	first, err := r.Commit(Message{Kind: KindClaim, Format: Format, Loop: "a-00000001", Target: "issue#42"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Commit(Message{Kind: KindClaim, Format: Format, Loop: "b-00000002", Target: "issue#42"})
	if err != nil {
		t.Fatal(err)
	}

	won, lostPush := r.Push(ref, first, Absent()), r.Push(ref, second, Absent())

	if won != nil {
		t.Fatalf("1 本目の push = %v", won)
	}
	if !errors.Is(lostPush, ErrLost) {
		t.Fatalf("2 本目の push = %v, 負けを期待", lostPush)
	}
	refs, err := r.List()
	if err != nil || refs[ref] != first {
		t.Fatalf("ref = %v (%v), 1 本目の %s を期待", refs, err, first)
	}
}

func TestClaimIsReplacedOnlyFromTheExpectedSHA(t *testing.T) {
	r := remoteForTest(t)
	ref := r.ClaimRef(target.Ref{Kind: target.KindCL, Number: 7})
	dead, _ := r.Commit(Message{Kind: KindClaim, Format: Format, Loop: "dead-00000001", Target: "cl#7"})
	mine, _ := r.Commit(Message{Kind: KindClaim, Format: Format, Loop: "me-00000002", Target: "cl#7"})
	other, _ := r.Commit(Message{Kind: KindClaim, Format: Format, Loop: "other-00000003", Target: "cl#7"})
	if err := r.Push(ref, dead, Absent()); err != nil {
		t.Fatal(err)
	}

	takeover, late := r.Push(ref, mine, Holding(dead)), r.Push(ref, other, Holding(dead))

	if takeover != nil {
		t.Fatalf("乗っ取り = %v", takeover)
	}
	if !errors.Is(late, ErrLost) {
		t.Fatalf("同じ旧 sha からの 2 本目 = %v, 負けを期待", late)
	}
}

func TestRefIsDeletedOnlyFromTheExpectedSHA(t *testing.T) {
	r := remoteForTest(t)
	ref := r.HeartbeatRef("a-00000001")
	first, _ := r.Commit(Message{Kind: KindHeartbeat, Format: Format, Loop: "a-00000001", Interval: time.Minute, Seq: 1})
	second, _ := r.Commit(Message{Kind: KindHeartbeat, Format: Format, Loop: "a-00000001", Interval: time.Minute, Seq: 2})
	if err := r.Push(ref, first, Unconditional()); err != nil {
		t.Fatal(err)
	}
	if err := r.Push(ref, second, Unconditional()); err != nil {
		t.Fatalf("条件を付けない置き直し = %v", err)
	}

	stale, deleted := r.Push(ref, "", Holding(first)), r.Push(ref, "", Holding(second))

	if !errors.Is(stale, ErrLost) || deleted != nil {
		t.Fatalf("古い sha からの削除 = %v, 今の sha からの削除 = %v", stale, deleted)
	}
	if refs, err := r.List(); err != nil || len(refs) != 0 {
		t.Fatalf("ref = %v (%v)", refs, err)
	}
}

func TestMessagesAreReadFromTheRemote(t *testing.T) {
	r := remoteForTest(t)
	m := Message{Kind: KindHeartbeat, Format: Format, Loop: "a-00000001", Interval: 5 * time.Minute, Seq: 3}
	sha, _ := r.Commit(m)
	if err := r.Push(r.HeartbeatRef("a-00000001"), sha, Unconditional()); err != nil {
		t.Fatal(err)
	}
	// 別の clone から読む (自分の clone に object がある前提にしない)
	reader := remoteForTest(t)
	reader.Git.Dir = filepath.Join(t.TempDir(), "reader")
	if out, err := exec.Command(reader.Git.Path, "init", "-q", reader.Git.Dir).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	bare, _ := r.Git.Output("remote", "get-url", "origin")
	if out, err := exec.Command(reader.Git.Path, "-C", reader.Git.Dir, "remote", "add", "origin", bare[:len(bare)-1]).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	messages, err := reader.Read([]string{sha})

	if err != nil || messages[sha] != m {
		t.Fatalf("読んだ = %+v (%v), 書いた = %+v", messages, err, m)
	}
}

func TestConfirmNamesAnUnknownRemote(t *testing.T) {
	r := remoteForTest(t)
	r.Name = "nowhere"

	_, err := r.Confirm()

	if err == nil {
		t.Fatal("解けない remote を通した")
	}
}
