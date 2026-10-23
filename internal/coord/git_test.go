package coord

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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
	return NewRemote("origin", "github.com_acme_widgets-1a2b3c4d", proc.Command{Path: git, Dir: clone, Env: env, Timeout: 30 * time.Second})
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

func TestConfirmRefusesARemoteThatPushesToAnotherURL(t *testing.T) {
	r := remoteForTest(t)
	if out, err := exec.Command(r.Git.Path, "-C", r.Git.Dir, "config", "remote.origin.pushurl", filepath.Join(t.TempDir(), "fork.git")).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	_, err := r.Confirm()

	if err == nil || !strings.Contains(err.Error(), "読む URL と書く URL が違う") {
		t.Fatalf("Confirm = %v, want 読む URL と書く URL の違いで失敗", err)
	}
}

func TestConfirmRefusesARemoteThatPushesToMoreThanOneURL(t *testing.T) {
	r := remoteForTest(t)
	url, _ := r.Git.Output("remote", "get-url", "origin")
	for _, push := range []string{strings.TrimSpace(url), filepath.Join(t.TempDir(), "mirror.git")} {
		if out, err := exec.Command(r.Git.Path, "-C", r.Git.Dir, "config", "--add", "remote.origin.pushurl", push).CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}

	_, err := r.Confirm()

	if err == nil || !strings.Contains(err.Error(), "読む URL と書く URL が違う") {
		t.Fatalf("Confirm = %v, want 書く URL が複数あることで失敗", err)
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

const batchSSH = "GIT_SSH_COMMAND=ssh -o BatchMode=yes"

func TestGitDisablesTheSSHPromptWhenTheUserLeavesSSHUnset(t *testing.T) {
	r := remoteForTest(t)

	if env := r.git().Env; !slices.Contains(env, batchSSH) {
		t.Fatalf("env = %v, %s を期待", env, batchSSH)
	}
}

func TestGitDecidesTheSSHCommandOnceAcrossCopies(t *testing.T) {
	r := remoteForTest(t)
	copied := r
	if env := r.git().Env; !slices.Contains(env, batchSSH) {
		t.Fatalf("env = %v, %s を期待", env, batchSSH)
	}
	// 判定の後に設定を変えても読み直さない (読み直すなら BatchMode が消える)
	if _, err := r.Git.Output("config", "core.sshCommand", "ssh -i key"); err != nil {
		t.Fatal(err)
	}

	if env := copied.git().Env; !slices.Contains(env, batchSSH) {
		t.Fatalf("env = %v, 写しが core.sshCommand を読み直した", env)
	}
}

func TestGitRereadsTheSSHCommandAfterAFailedRead(t *testing.T) {
	dir := t.TempDir()
	// 1 回目の読み出しは exit 128 で失敗し、2 回目以降は設定が無い (exit 1)
	stub := filepath.Join(dir, "git")
	script := "#!/bin/sh\nif [ -e " + dir + "/failed ]; then exit 1; fi\ntouch " + dir + "/failed\nexit 128\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewRemote("origin", "scope", proc.Command{Path: stub, Dir: dir, Timeout: 30 * time.Second})

	if env := r.git().Env; slices.Contains(env, batchSSH) {
		t.Fatalf("env = %v, 読めなかったのに BatchMode を足した", env)
	}
	if env := r.git().Env; !slices.Contains(env, batchSSH) {
		t.Fatalf("env = %v, 読めなかった判定を持ち続けた", env)
	}
}

func TestGitKeepsTheUsersSSHCommand(t *testing.T) {
	for name, set := range map[string]func(t *testing.T, r *Remote){
		"GIT_SSH_COMMAND": func(t *testing.T, r *Remote) { r.Git.Env = append(r.Git.Env, "GIT_SSH_COMMAND=ssh -i key") },
		"GIT_SSH":         func(t *testing.T, r *Remote) { r.Git.Env = append(r.Git.Env, "GIT_SSH=/bin/ssh-wrapper") },
		"core.sshCommand": func(t *testing.T, r *Remote) {
			if _, err := r.Git.Output("config", "core.sshCommand", "ssh -i key"); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := remoteForTest(t)
			set(t, &r)

			if env := r.git().Env; slices.Contains(env, batchSSH) {
				t.Fatalf("env = %v, 利用者の設定を上書きした", env)
			}
		})
	}
}
