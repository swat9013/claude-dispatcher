package coord

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/target"
)

// Party を、実物の git と一時 dir の bare repo の coordination board に繋ぎ、Board が使う口 (Refs) 越しに撃つ。

var issue42 = target.Ref{Kind: target.KindIssue, Number: 42}

// countingFetches は r の git を、fetch を撃つたびに 1 行を書いてから実物の git を呼ぶ script に差し替え、撃った回数を返す
// 関数を返す。log は差し替えた時点で作り、script は撃たれるたびに log に書けることを確かめる (書けなければ git を撃たずに
// 落ちる) ので、数えられない状態が 0 回に見えることは無い。
func countingFetches(t *testing.T, r *Remote) func() int {
	t.Helper()
	log := filepath.Join(t.TempDir(), "fetches")
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	wrapGit(t, r, "set -e\n: >> "+shQuote(log)+"\n", "fetch", "echo >> "+shQuote(log))
	return func() int {
		b, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		return bytes.Count(b, []byte("\n"))
	}
}

// wrapGit は r の git を、prelude を撃ってから、引数に sub を含めば onMatch を撃ち、実物の git を呼ぶ sh の script に差し替える。
func wrapGit(t *testing.T, r *Remote, prelude, sub, onMatch string) {
	t.Helper()
	wrapper := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\n" + prelude + "for a in \"$@\"; do if [ \"$a\" = " + shQuote(sub) + " ]; then " + onMatch +
		"; fi; done\nexec " + shQuote(r.Git.Path) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r.Git.Path = wrapper
}

// shQuote は s を sh の single quote で囲む。
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func TestASecondObservationFetchesNothing(t *testing.T) {
	r := remoteForTest(t)
	other := &Party{Remote: cloneForTest(t, bareOf(t, r)), Loop: "other-00000002"}
	if _, err := other.Claim(issue42, Absent()); err != nil {
		t.Fatal(err)
	}
	if err := other.Heartbeat(time.Minute); err != nil {
		t.Fatal(err)
	}
	fetches := countingFetches(t, &r)
	var me Refs = &Party{Remote: r, Loop: "me-00000001"}
	if _, err := me.Observe(); err != nil {
		t.Fatal(err)
	}
	before := fetches()
	if before != 1 {
		t.Fatalf("1 回目の fetch %d 回, want 1 回 (新しく見た 2 つの sha をまとめて読む)", before)
	}

	v, err := me.Observe()

	if err != nil {
		t.Fatal(err)
	}
	if n := fetches() - before; n != 0 {
		t.Fatalf("2 回目の fetch %d 回, want 0 回 (覚えた commit message を使う)", n)
	}
	if len(v.Messages) != 2 || v.Messages[v.Claims[issue42].SHA].Loop != "other-00000002" {
		t.Fatalf("2 回目の commit message = %+v, want 着手宣言と生存報告の 2 つ", v.Messages)
	}
}

func TestObserveNamesEachRefAsItIsSpelledOnTheRemote(t *testing.T) {
	r := remoteForTest(t)
	me := &Party{Remote: r, Loop: "me-00000001"}
	if _, err := me.Claim(issue42, Absent()); err != nil {
		t.Fatal(err)
	}
	if err := me.Heartbeat(time.Minute); err != nil {
		t.Fatal(err)
	}
	var refs Refs = me

	v, err := refs.Observe()

	if err != nil {
		t.Fatal(err)
	}
	if got, want := v.Claims[issue42].Name, r.ClaimRef(issue42); got != want {
		t.Fatalf("着手宣言の ref の名前 = %q, want %q", got, want)
	}
	if got, want := v.Loops["me-00000001"].Name, r.HeartbeatRef("me-00000001"); got != want {
		t.Fatalf("生存報告の ref の名前 = %q, want %q", got, want)
	}
}

func TestACommitWhoseRefWentAwayIsFetchedAgainWhenItComesBack(t *testing.T) {
	r := remoteForTest(t)
	otherRemote := cloneForTest(t, bareOf(t, r))
	other := &Party{Remote: otherRemote, Loop: "other-00000002"}
	sha, err := other.Claim(issue42, Absent())
	if err != nil {
		t.Fatal(err)
	}
	fetches := countingFetches(t, &r)
	var me Refs = &Party{Remote: r, Loop: "me-00000001"}
	if _, err := me.Observe(); err != nil {
		t.Fatal(err)
	}
	if err := other.DeleteClaim(issue42, sha); err != nil {
		t.Fatal(err)
	}
	// ref が消えた tick に、その sha の commit message を忘れる
	if _, err := me.Observe(); err != nil {
		t.Fatal(err)
	}
	if err := otherRemote.Push(otherRemote.ClaimRef(issue42), sha, Absent()); err != nil {
		t.Fatal(err)
	}

	v, err := me.Observe()

	if err != nil {
		t.Fatal(err)
	}
	if n := fetches(); n != 2 {
		t.Fatalf("fetch %d 回, want 2 回 (ref が消えた間に忘れた commit を読み直す)", n)
	}
	if v.Messages[sha].Loop != "other-00000002" {
		t.Fatalf("読み直した commit message = %+v", v.Messages[sha])
	}
}

func TestMyOwnHeartbeatIsKeptWhileItsRefIsAway(t *testing.T) {
	r := remoteForTest(t)
	fetches := countingFetches(t, &r)
	var me Refs = &Party{Remote: r, Loop: "me-00000001"}
	if err := me.Heartbeat(time.Minute); err != nil {
		t.Fatal(err)
	}
	placed, err := me.Observe()
	if err != nil {
		t.Fatal(err)
	}
	name, sha := r.HeartbeatRef("me-00000001"), placed.Loops["me-00000001"].SHA
	// 置き直したばかりの生存報告がまだ ref に載らない間の代役として、ref だけを外して戻す
	if err := r.Push(name, "", Holding(sha)); err != nil {
		t.Fatal(err)
	}
	if _, err := me.Observe(); err != nil {
		t.Fatal(err)
	}
	if err := r.Push(name, sha, Absent()); err != nil {
		t.Fatal(err)
	}

	v, err := me.Observe()

	if err != nil {
		t.Fatal(err)
	}
	if n := fetches(); n != 0 {
		t.Fatalf("fetch %d 回, want 0 回 (自分の生存報告の commit message は ref に無い間も覚えている)", n)
	}
	if v.Messages[sha].Loop != "me-00000001" {
		t.Fatalf("自分の生存報告の commit message = %+v", v.Messages[sha])
	}
}

func TestMyOlderHeartbeatIsForgottenOnceItIsReplaced(t *testing.T) {
	r := remoteForTest(t)
	fetches := countingFetches(t, &r)
	var me Refs = &Party{Remote: r, Loop: "me-00000001"}
	if err := me.Heartbeat(time.Minute); err != nil {
		t.Fatal(err)
	}
	older, err := me.Observe()
	if err != nil {
		t.Fatal(err)
	}
	if err := me.Heartbeat(time.Minute); err != nil {
		t.Fatal(err)
	}
	// 置き直した後の Observe で、ref に無い古い生存報告を忘れる
	if _, err := me.Observe(); err != nil {
		t.Fatal(err)
	}
	sha := older.Loops["me-00000001"].SHA
	if err := r.Push(r.HeartbeatRef("me-00000001"), sha, Unconditional()); err != nil {
		t.Fatal(err)
	}
	before := fetches()

	v, err := me.Observe()

	if err != nil {
		t.Fatal(err)
	}
	if v.Messages[sha].Loop != "me-00000001" {
		t.Fatalf("戻った古い生存報告の commit message = %+v", v.Messages[sha])
	}
	if n := fetches() - before; n != 1 {
		t.Fatalf("fetch %d 回, want 1 回 (覚えるのは最後に置けた自分の生存報告だけで、古いものは読み直す)", n)
	}
}

func TestDeleteClaimLosesToAClaimOfAnotherSHA(t *testing.T) {
	r := remoteForTest(t)
	other := &Party{Remote: cloneForTest(t, bareOf(t, r)), Loop: "other-00000002"}
	theirs, err := other.Claim(issue42, Absent())
	if err != nil {
		t.Fatal(err)
	}
	stale, err := r.Commit(Message{Kind: KindClaim, Format: Format, Loop: "me-00000001", Target: "issue#42"})
	if err != nil {
		t.Fatal(err)
	}
	var me Refs = &Party{Remote: r, Loop: "me-00000001"}

	err = me.DeleteClaim(issue42, stale)

	if !errors.Is(err, ErrLost) {
		t.Fatalf("DeleteClaim = %v, want 負け", err)
	}
	if refs, err := r.List(); err != nil || refs[r.ClaimRef(issue42)] != theirs {
		t.Fatalf("ref = %v (%v), want 他の loop の着手宣言 %s が残る", refs, err, theirs)
	}
}

func TestLeaveDeletesTheLastHeartbeatPlacedEvenAfterARenewalFailed(t *testing.T) {
	r := remoteForTest(t)
	var me Refs = &Party{Remote: r, Loop: "me-00000001"}
	if err := me.Heartbeat(time.Minute); err != nil {
		t.Fatal(err)
	}
	// bare repo を一時的に外して、置き直しの push を落とす
	bare := bareOf(t, r)
	if err := os.Rename(bare, bare+".away"); err != nil {
		t.Fatal(err)
	}
	renewal := me.Heartbeat(time.Minute)
	if err := os.Rename(bare+".away", bare); err != nil {
		t.Fatal(err)
	}
	if renewal == nil {
		t.Fatal("置き直しが落ちなかった")
	}

	err := me.Leave()

	if err != nil {
		t.Fatalf("Leave = %v", err)
	}
	if refs, err := r.List(); err != nil || len(refs) != 0 {
		t.Fatalf("ref = %v (%v), want 最後に置けた生存報告を消す", refs, err)
	}
}

func TestLeaveLosesWhenAnotherLoopDeletedMyHeartbeat(t *testing.T) {
	r := remoteForTest(t)
	var me Refs = &Party{Remote: r, Loop: "me-00000001"}
	if err := me.Heartbeat(time.Minute); err != nil {
		t.Fatal(err)
	}
	// 他のマシンの loop が、自分を死んだとみなして生存報告を消す
	other := &Party{Remote: cloneForTest(t, bareOf(t, r)), Loop: "other-00000002"}
	seen, err := other.Observe()
	if err != nil {
		t.Fatal(err)
	}
	if err := other.DeleteHeartbeat("me-00000001", seen.Loops["me-00000001"].SHA); err != nil {
		t.Fatal(err)
	}

	err = me.Leave()

	if !errors.Is(err, ErrLost) {
		t.Fatalf("Leave = %v, want 負け", err)
	}
}

func TestLeaveKeepsAHeartbeatItDidNotPlace(t *testing.T) {
	r := remoteForTest(t)
	var me Refs = &Party{Remote: r, Loop: "me-00000001"}
	if err := me.Heartbeat(time.Minute); err != nil {
		t.Fatal(err)
	}
	// 同じ loop 名の別の起動 (別の clone) が、生存報告の ref を自分の置いていない sha に置き換える
	impostor := cloneForTest(t, bareOf(t, r))
	name := impostor.HeartbeatRef("me-00000001")
	theirs, err := impostor.Commit(Message{Kind: KindHeartbeat, Format: Format, Loop: "me-00000001", Interval: time.Minute, Seq: 99})
	if err != nil {
		t.Fatal(err)
	}
	if err := impostor.Push(name, theirs, Unconditional()); err != nil {
		t.Fatal(err)
	}

	err = me.Leave()

	if !errors.Is(err, ErrLost) {
		t.Fatalf("Leave = %v, want 負け", err)
	}
	if refs, err := r.List(); err != nil || refs[name] != theirs {
		t.Fatalf("ref = %v (%v), want 自分の置いていない生存報告 %s が残る", refs, err, theirs)
	}
}

// failingGit は r の git を、引数に sub を含むと git を撃たずに落ち、それ以外は実物の git を呼ぶ script に差し替える。
func failingGit(t *testing.T, r *Remote, sub string) {
	t.Helper()
	wrapGit(t, r, "", sub, "echo "+shQuote(sub+" が落ちた")+" >&2; exit 1")
}

func TestAClaimThatFailsBeforePushIsToldApartFromAFailedPush(t *testing.T) {
	t.Run("着手宣言の commit を作れなければ、push より前の失敗を返して push しない", func(t *testing.T) {
		r := remoteForTest(t)
		failingGit(t, &r, "commit-tree")
		var me Refs = &Party{Remote: r, Loop: "me-00000001"}

		_, err := me.Claim(issue42, Absent())

		if !errors.Is(err, ErrNotPushed) {
			t.Fatalf("Claim = %v, want push より前の失敗", err)
		}
		if refs, err := r.List(); err != nil || len(refs) != 0 {
			t.Fatalf("ref = %v (%v), want 何も置かない", refs, err)
		}
	})
	t.Run("push が負け以外で落ちれば、push より前の失敗にしない", func(t *testing.T) {
		r := remoteForTest(t)
		failingGit(t, &r, "push")
		var me Refs = &Party{Remote: r, Loop: "me-00000001"}

		_, err := me.Claim(issue42, Absent())

		if err == nil || errors.Is(err, ErrNotPushed) || errors.Is(err, ErrLost) {
			t.Fatalf("Claim = %v, want 結果の分からない push の失敗", err)
		}
	})
}
