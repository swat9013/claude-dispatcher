package places

import (
	"errors"
	"fmt"

	"github.com/swat9013/claude-dispatcher/internal/provider"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// Store は loop・試運転・doctor が読む置き場: issue 置き場の部品と CL 置き場の部品 (system.md §13)。本番は provider が組み立てた
// adapter、テストは in-memory。失敗は *target.Failure。一覧は *target.NonFatal を一覧とともに返しうる (OpenItems)。
type Store struct {
	Issues provider.IssueStore
	// CLs は CL 置き場の部品。検査に通った workflow 定義は CL 置き場をちょうど 1 つ持つ (ADR 0015) ので、いつも組まれる
	CLs provider.CLStore
	// slow は一覧の読み出しが遅いときの知らせ方 (WithSlow)。ゼロ値なら知らせない
	slow SlowNotice
}

// WithSlow は、一覧の読み出しが遅いときの知らせ方を n にした置き場。
func (s Store) WithSlow(n SlowNotice) Store {
	s.slow = n
	return s
}

// Open は種類の open な作業対象を、その種類の置き場の部品で全件読む。読み出しが SlowListing を超えたら、読み終わる前に
// 知らせる (WithSlow)。知らせても読み出しは止めず、結果も変えない。
func (s Store) Open(kind target.Kind) ([]target.Item, error) {
	var read func() ([]target.Item, error)
	switch kind {
	case target.KindIssue:
		read = s.Issues.OpenIssues
	case target.KindCL:
		read = s.CLs.OpenCLs
	default:
		return nil, fmt.Errorf("未知の作業対象の種類 %q", kind)
	}
	var items []target.Item
	var err error
	s.slow.Watch(kind, func() { items, err = read() })
	return items, err
}

// Read は作業対象 1 件を、その種類の置き場の部品で読み直す。終端になっているか消えていれば Terminal。
func (s Store) Read(ref target.Ref) (target.Item, error) {
	switch ref.Kind {
	case target.KindIssue:
		return s.Issues.ReadIssue(ref.Number)
	case target.KindCL:
		return s.CLs.ReadCL(ref.Number)
	}
	return nil, fmt.Errorf("未知の作業対象の種類 %q", ref.Kind)
}

// OpenItems は、workflow 定義の trigger に現れる種類の open な作業対象を置き場から全件読む。置き場の部品が作業対象とともに
// 処理を続けられる失敗 (*target.NonFatal) を返したら、作業対象を読み進め、その失敗をまとめて作業対象とともに返す。
func OpenItems(store Store, def workflow.Definition) ([]target.Item, error) {
	items := []target.Item{}
	var nonFatal []error
	for _, kind := range def.Kinds() {
		open, err := store.Open(kind)
		var failure *target.NonFatal
		switch {
		case errors.As(err, &failure):
			nonFatal = append(nonFatal, err)
		case err != nil:
			return nil, err
		}
		items = append(items, open...)
	}
	return items, errors.Join(nonFatal...)
}
