package coord

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/target"
)

// Party を、実物の git と一時 dir の bare repo の coordination board に繋ぎ、Board が使う口 (Refs) 越しに撃つ。

var issue42 = target.Ref{Kind: target.KindIssue, Number: 42}

// countingFetches は r の git を、fetch を撃つたびに 1 行を書いてから実物の git を呼ぶ script に差し替え、撃った回数を返す
// 関数を返す。
func countingFetches(t *testing.T, r *Remote) func() int {
	t.Helper()
	dir := t.TempDir()
	log, wrapper := filepath.Join(dir, "fetches"), filepath.Join(dir, "git")
	script := fmt.Sprintf("#!/bin/sh\nfor a in \"$@\"; do if [ \"$a\" = fetch ]; then echo >> %q; fi; done\nexec %q \"$@\"\n", log, r.Git.Path)
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r.Git.Path = wrapper
	return func() int {
		b, err := os.ReadFile(log)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		return bytes.Count(b, []byte("\n"))
	}
}

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

	if _, err := me.Observe(); err != nil {
		t.Fatal(err)
	}

	if n := fetches(); n != 1 {
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
