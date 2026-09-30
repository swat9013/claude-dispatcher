// Package target は作業対象 (open な issue) の正規化した形と鍵と、置き場の部品が返す分類済みの失敗を持つ (system.md §13)。
// trigger の評価は、この形だけを見る。adapter の生の応答を覗かない。
package target

import (
	"fmt"
	"strconv"
	"strings"
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

// Kind は作業対象の種類。
type Kind string

// KindIssue は issue の作業対象
const KindIssue Kind = "issue"

// kinds は作業対象の種類の全部 (置き場の名前から種類を読むときに使う)
var kinds = []Kind{KindIssue}

// Ref は作業対象を指す鍵 (種類と番号)。claim と workspace はこの鍵で持つ。
type Ref struct {
	Kind   Kind
	Number int
}

// String は作業対象の表示名 (log の `target` の値。`issue#42` の綴り)。
func (r Ref) String() string { return fmt.Sprintf("%s#%d", r.Kind, r.Number) }

// FileName は作業対象ごとの file と dir の名前 (workspace・描画した共通 prompt・worker log。`issue-42` の綴り)。
func (r Ref) FileName() string { return fmt.Sprintf("%s-%d", r.Kind, r.Number) }

// ParseFileName は FileName の綴りの name から作業対象を読む。その綴りでなければ false。
func ParseFileName(name string) (Ref, bool) {
	for _, kind := range kinds {
		digits, ok := strings.CutPrefix(name, string(kind)+"-")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(digits)
		if ref := (Ref{Kind: kind, Number: n}); err == nil && ref.FileName() == name {
			return ref, true
		}
	}
	return Ref{}, false
}

// Item は正規化した作業対象 1 件 (Issue)。
type Item interface {
	Ref() Ref
	// Terminal は作業対象が終端か
	Terminal() bool
	// Created は作成日時 (候補の並べ方に使う)
	Created() time.Time
	// Heading は題名
	Heading() string
}

func (i Issue) Ref() Ref           { return Ref{Kind: KindIssue, Number: i.Number} }
func (i Issue) Terminal() bool     { return i.Closed }
func (i Issue) Created() time.Time { return i.CreatedAt }
func (i Issue) Heading() string    { return i.Title }

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
