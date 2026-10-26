package coord

import (
	"github.com/swat9013/claude-dispatcher/internal/target"
)

// Outcome は Board が撃った push と削除と、coordination board を読んで知ったことのうち、呼び出し側が行にして残すもの。部品は
// log を書かない (system.md §13)。行の event 名・field・画面の文は呼び出し側が決める。
type Outcome interface{ outcome() }

// Unread は、ref の指す commit の commit message を読めなかったこと。処理は続ける。
type Unread struct{ Err error }

// OwnerUnreadable は、持ち主の loop を読めない着手宣言を初めて見たこと (その作業対象は起動しない)。Name は ref の名前。
type OwnerUnreadable struct {
	Ref  target.Ref
	Name string
}

// IntervalUnreadable は、間隔を読めない生存報告を初めて見たこと (その loop の死は判定しない)。Name は ref の名前。
type IntervalUnreadable struct {
	Loop string
	Name string
}

// TakenOver は、自分の着手宣言の sha が変わったか消えていたこと。着手宣言はもう自分のものではないので、Board は消さない。
type TakenOver struct {
	Ref target.Ref
	// By は今の持ち主の loop 名。着手宣言が消えていたか、持ち主を読めないときは ""
	By string
	// Unreadable は、着手宣言が持ち主を読めないものに置き換わっていたこと
	Unreadable bool
}

// InheritReplaced は、orphan のために引き継いだ着手宣言が他の loop のものに置き換わったこと (orphan の claude と二重に動きうる)。
type InheritReplaced struct{ Ref target.Ref }

// StaleDeleteFailed は、消し損ねた自分の着手宣言を消せなかったこと。
type StaleDeleteFailed struct {
	Ref target.Ref
	Err error
}

// ClaimPlaced は着手宣言を置けたこと。ClaimTookOver は死んだ loop の着手宣言を旧 sha を期待値にして乗っ取れたこと。
// ClaimLost は他の loop に先を越されたこと。ClaimFailed はそれ以外の失敗 (次の tick で試み直す)。Claim が返す。
type (
	ClaimPlaced   struct{}
	ClaimTookOver struct{ From string }
	ClaimLost     struct{}
	ClaimFailed   struct{ Err error }
)

// Inherited は、生きている orphan の作業対象の着手宣言を、旧 sha を期待値にした置き換えで自分の名前に引き継げたこと。
type Inherited struct {
	Ref  target.Ref
	From string
}

// InheritFailed は、引き継ぎの着手宣言を置けなかったこと (負けを除く)。push が結果の分からない失敗をしたときは、remote に
// 届いていたかもしれないので Board は覚えておく。push より前に落ちた (ErrNotPushed) ときは覚えない。
type InheritFailed struct {
	Ref target.Ref
	Err error
}

// DeadRefDeleteFailed は、死んだ loop の ref を消せなかったこと (負けは読み飛ばし、これにならない)。Ref は着手宣言の作業対象で、
// 生存報告なら零値。
type DeadRefDeleteFailed struct {
	Ref  target.Ref
	Loop string
	Err  error
}

// DeadLoopCleaned は、死んだ loop の ref を消したこと (1 つの loop につき 1 つ)。Refs は消した ref の名前、Heartbeat は生存報告を
// 消したか、Claims は消した着手宣言の数。
type DeadLoopCleaned struct {
	Loop      string
	Refs      []string
	Heartbeat bool
	Claims    int
}

// InheritedReleaseFailed は、orphan が終わった作業対象の引き継いだ着手宣言を消せなかったこと (覚えたままにして消し直す)。
type InheritedReleaseFailed struct {
	Ref target.Ref
	Err error
}

// ClaimReleaseFailed は、claim を解いた作業対象の自分の着手宣言を消せなかったこと (次の突き合わせで消し損ねとして消し直す)。
type ClaimReleaseFailed struct {
	Ref target.Ref
	Err error
}

// InheritKept は、止まるときに引き継いだ着手宣言を残したこと。OrphanAlive は orphan がまだ生きている (作業を続けうる) か。
// 生きていなければ、orphan は終わったが消せなかった。UnsureInheritKept は引き継ぎの結果が分からないまま、届いていたかもしれない
// 着手宣言を残したこと。OrphanAlive は orphan がまだ生きているか。生きていなければ orphan は終わっていたが、sha を知らないので
// 消そうとしていない (消し損ねた InheritKept とは違う)。
type (
	InheritKept struct {
		Ref         target.Ref
		OrphanAlive bool
	}
	UnsureInheritKept struct {
		Ref         target.Ref
		OrphanAlive bool
	}
)

// ClaimReleaseLost は、claim を解いた作業対象の自分の着手宣言の削除に負けたこと (他の loop が先に消したか乗っ取った)。消し直さない。
type ClaimReleaseLost struct {
	Ref target.Ref
	Err error
}

// LeaveFailed は、止まるときに自分の生存報告を消せなかったこと。
type LeaveFailed struct{ Err error }

// BeatFailed は生存報告の置き直しが落ちたこと。BeatsDropped は置き直しの結果を、受け取り手が読めないほど溜まって N 件捨てたこと。
type (
	BeatFailed   struct{ Err error }
	BeatsDropped struct{ N int }
)

func (Unread) outcome()                 {}
func (OwnerUnreadable) outcome()        {}
func (IntervalUnreadable) outcome()     {}
func (TakenOver) outcome()              {}
func (InheritReplaced) outcome()        {}
func (StaleDeleteFailed) outcome()      {}
func (ClaimPlaced) outcome()            {}
func (ClaimTookOver) outcome()          {}
func (ClaimLost) outcome()              {}
func (ClaimFailed) outcome()            {}
func (Inherited) outcome()              {}
func (InheritFailed) outcome()          {}
func (DeadRefDeleteFailed) outcome()    {}
func (DeadLoopCleaned) outcome()        {}
func (InheritedReleaseFailed) outcome() {}
func (ClaimReleaseFailed) outcome()     {}
func (ClaimReleaseLost) outcome()       {}
func (InheritKept) outcome()            {}
func (UnsureInheritKept) outcome()      {}
func (LeaveFailed) outcome()            {}
func (BeatFailed) outcome()             {}
func (BeatsDropped) outcome()           {}
