// Package target は作業対象 (open な issue) の正規化した形と、置き場の部品が返す分類済みの失敗を持つ (system.md §13)。
// trigger の評価は、この形だけを見る。adapter の生の応答を覗かない。
package target

import (
	"fmt"
	"time"
)

// Issue は open な issue 1 件。tracker の綴りは adapter が写し終えている。
type Issue struct {
	Number int
	Title  string
	URL    string
	// Closed は issue が終端 (close) か。open な issue の一覧からは常に false
	Closed    bool
	CreatedAt time.Time
	Labels    []string
	Assignees []string
	// AuthorIsCollaborator は作者が collaborator (repo の owner・organization の member・collaborator) か
	AuthorIsCollaborator bool
	// Milestone は milestone の題名。milestone に入っていなければ ""
	Milestone string
	// OpenBlockers は未解決 (open) の依存先 (blocked by) の数
	OpenBlockers int
}

// FailureKind は観測の失敗の分類 (SPEC §11.4)。
type FailureKind int

const (
	// Unavailable は下の分類に入らない失敗 (起動できない・timeout・network・読めない応答)
	Unavailable FailureKind = iota
	// Auth は認証が通らない
	Auth
	// NotVisible は置き場が見えない (綴りの誤りか、権限が無い)
	NotVisible
	// Truncated は 1 往復で読む件数の上限を超え、読み切れない
	Truncated
	// RateLimit は rate limit に当たった
	RateLimit
)

func (k FailureKind) String() string {
	switch k {
	case Auth:
		return "認証が通らない"
	case NotVisible:
		return "見えない"
	case Truncated:
		return "読み切れない"
	case RateLimit:
		return "rate limit"
	default:
		return "読めない"
	}
}

// Failure は置き場の部品が返す観測の失敗。
type Failure struct {
	Kind FailureKind
	// Place は読もうとした置き場 (`acme/widgets` など)
	Place string
	Err   error
}

func (f *Failure) Error() string {
	return fmt.Sprintf("置き場 %s を観測できない (%s): %v", f.Place, f.Kind, f.Err)
}

func (f *Failure) Unwrap() error { return f.Err }
