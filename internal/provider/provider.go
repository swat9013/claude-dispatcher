// Package provider は tracker の種類 (`tracker.kind`) の知識を、サービスごとの provider 1 つに閉じる (system.md §13)。
// workflow 定義の検査・setup・doctor・loop の組み立ては、種類で分岐せずに provider を引く。種類を足すときは表に provider を
// 1 つ足す。provider は seam ではない (adapter を差し替える口ではなく、どの adapter を組むかを種類から引く表)。
package provider

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/scaffold"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
)

// Kind は tracker の種類 (formats.md §2.1 の tracker.kind)。
type Kind string

const (
	GitHub Kind = "github"
	GitLab Kind = "gitlab"
	Jira   Kind = "jira"
)

// Kinds は provider を持つ種類を、表 (providers) の順に返す。
func Kinds() []Kind {
	kinds := make([]Kind, len(providers))
	for i, p := range providers {
		kinds[i] = p.Kind
	}
	return kinds
}

// Join は種類の列を誤りの文言に出す綴り (`github / gitlab`) にする。
func Join(kinds []Kind) string {
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}
	return strings.Join(names, " / ")
}

// Of は種類の provider。表に無い種類なら false。
func Of(kind Kind) (Provider, bool) {
	for _, p := range providers {
		if p.Kind == kind {
			return p, true
		}
	}
	return Provider{}, false
}

// CLHostKinds は CL 置き場の役割を持つ種類を、表 (providers) の順に返す。cl.kind に書けるのはこれだけ。
func CLHostKinds() []Kind {
	var kinds []Kind
	for _, p := range providers {
		if p.CLHost != nil {
			kinds = append(kinds, p.Kind)
		}
	}
	return kinds
}

// Feature は、種類によって書けたり書けなかったりする workflow 定義の宣言。
type Feature string

const (
	// Token は tracker.token
	Token Feature = "tracker.token"
	// CLPlace は issue 置き場と別の CL 置き場の宣言 (cl:)
	CLPlace Feature = "cl"
	// Author・Milestone・Blocked・Status・Type は述語 (Author は issue 側と CL 側の両方にある)
	Author    Feature = "author"
	Milestone Feature = "milestone"
	Blocked   Feature = "blocked"
	Status    Feature = "status"
	Type      Feature = "type"
)

// TrackerFeatures は issue 置き場の役割が支えるかを宣言する宣言の全件、CLHostFeatures は CL 置き場の役割のもの。どの
// provider も、持つ役割ごとに、すべてを支えるか支えないかのどちらかで宣言する (テストが確かめる)。
var (
	TrackerFeatures = []Feature{Token, CLPlace, Author, Milestone, Blocked, Status, Type}
	CLHostFeatures  = []Feature{Author}
)

// Declarations は役割が支える宣言 (Supports) と、支えない宣言とその理由の文言 (Declines。誤りの名指しに添える)。
type Declarations struct {
	Supports []Feature
	Declines map[Feature]string
}

// Supported は宣言 f を支えるか。支える宣言の無いものは支えない (fail closed)。
func (d Declarations) Supported(f Feature) bool { return slices.Contains(d.Supports, f) }

// DeclineReason は支えない宣言 f の理由の文言。理由を宣言していなければ、支える宣言が無いことを理由にする。
func (d Declarations) DeclineReason(f Feature) string {
	if reason := d.Declines[f]; reason != "" {
		return reason
	}
	return "provider に支える宣言が無い"
}

// Provider はサービス 1 つの知識。データは field に持ち、置き場の読み方と部品の組み立てだけを関数の field に持つ。
type Provider struct {
	Kind Kind
	// Command は役割を担う CLI
	Command string
	Host    Host
	// ParseRepo は tracker.repo の綴りを確かめて揃えた値を返す
	ParseRepo func(string) (string, error)
	// NewPlace は Host と ParseRepo を通った host と repo から置き場を作る (host を書けない種類は "")
	NewPlace func(host, repo string) Place
	// Tracker は issue 置き場の役割。どの種類も持つ
	Tracker TrackerRole
	// CLHost は CL 置き場の役割。持たない種類は nil (CL 置き場は cl: で別の種類を宣言する)
	CLHost *CLHostRole
	// Build は置き場の部品を組み立てる
	Build func(Runtime, Setting) Parts
	Setup Setup
}

// Host は tracker.host の読み方。
type Host struct {
	// Parse は tracker.host の綴りを確かめる。nil なら tracker.host を書けない
	Parse func(string) (string, error)
	// Default は書かなかったときの host。"" なら必須
	Default string
	// RequiredHint は必須の項目の欠落に添える案内
	RequiredHint string
}

// Writable は tracker.host を書けるか。
func (h Host) Writable() bool { return h.Parse != nil }

// Required は tracker.host が必須か (書けて、書かなかったときの既定を持たない)。
func (h Host) Required() bool { return h.Writable() && h.Default == "" }

// TrackerRole は issue 置き場の役割の宣言。
type TrackerRole struct {
	// KeyedIssues は issue を key (`WIDGETS-123`) で指すか。真なら `.issue.key` が使える
	KeyedIssues bool
	// Prefix は key で指さない種類の issue を、人が読む行に出すときに番号の前に置く綴り
	Prefix string
	// IssueKey は key で指す種類の、番号の issue の key
	IssueKey func(place Place, number int) string
	// declared は宣言。tracker.token を支えるかだけは Supports に書かず TokenVariable から導く (Declarations)。支えない種類
	// の理由は Declines に書く
	declared Declarations
	// TokenVariable は tracker.token を渡す環境変数の名前 (役割を担う CLI が読むもの)。"" でない種類だけが tracker.token を
	// 支える
	TokenVariable string
	// Settings は worker が issue 置き場を操作するのに要りそうな Claude Code の settings の entry
	Settings []string
}

// Declarations は issue 置き場の役割の宣言。tracker.token は、渡す環境変数を持つ種類なら支える宣言に足す。
func (r TrackerRole) Declarations() Declarations {
	if r.TokenVariable == "" {
		return r.declared
	}
	return Declarations{Supports: append(slices.Clone(r.declared.Supports), Token), Declines: r.declared.Declines}
}

// CLHostRole は CL 置き場の役割の宣言。
type CLHostRole struct {
	// Prefix は CL を人が読む行に出すときに番号の前に置く綴り
	Prefix string
	Declarations
	// Settings は worker が CL 置き場を操作するのに要りそうな Claude Code の settings の entry
	Settings []string
	// Scaffold は置き場を雛形の cl: と CL 側の trigger の書き方にする
	Scaffold func(Place) scaffold.CLHost
}

// CommonSettings は役割に依らず worker が使いそうな settings の entry。
var CommonSettings = []string{"Bash(git push:*)"}

// WithToken は env に tracker.token の値 token を、CLI が読む環境変数として置いた env を返す (system.md §8)。env に同じ
// 変数が既にあっても token で上書きする。token が "" なら env のまま。tracker.token を支えない種類 (TokenVariable が "")
// の token は、workflow 定義の検査が弾くので届かない。届けば呼び出し側の誤りなので panic する。
func (p Provider) WithToken(env []string, token string) []string {
	if token == "" {
		return env
	}
	if p.Tracker.TokenVariable == "" {
		panic(fmt.Sprintf("tracker.kind: %s の provider は tracker.token を渡す環境変数を持たない", p.Kind))
	}
	return deps.WithEnv(env, map[string]string{p.Tracker.TokenVariable: token})
}

// Settings は worker が外部 store を操作するのに要りそうな settings の entry (issue 置き場の役割・CL 置き場の役割・共通の
// 順)。clHost は CL 置き場の役割を担う provider の役割。
func Settings(tracker TrackerRole, clHost CLHostRole) []string {
	entries := append(append([]string{}, tracker.Settings...), clHost.Settings...)
	return append(entries, CommonSettings...)
}

// Reference は作業対象を人が読む行に出す参照 (formats.md §5)。呼び方は作業対象の種類の役割が宣言する。CL は CL 置き場の
// 役割を担う provider で引く。役割を持たない provider に渡せば呼び出し側の誤りなので panic する。
func (p Provider) Reference(place Place, ref target.Ref) string {
	switch {
	case ref.Kind == target.KindIssue && p.Tracker.KeyedIssues:
		return p.Tracker.IssueKey(place, ref.Number)
	case ref.Kind == target.KindIssue:
		return p.Tracker.Prefix + strconv.Itoa(ref.Number)
	case ref.Kind == target.KindCL && p.CLHost != nil:
		return p.CLHost.Prefix + strconv.Itoa(ref.Number)
	}
	panic(fmt.Sprintf("tracker.kind: %s の provider は %s を指す役割を持たない", p.Kind, ref))
}

// Place は provider が tracker.repo と tracker.host から読んだ置き場。表示名を返し、その provider の組み立てに渡す。
type Place struct {
	name string
	// value は adapter の置き場の値。組み立てる provider だけが型を知る
	value any
}

// String は置き場の表示名 (github は `owner/name`、gitlab は `<host>/<path>`、jira は `<site>/<project key>`)。
func (p Place) String() string { return p.name }

// Runtime は部品が撃つ CLI の実行環境。
type Runtime struct {
	// Env は CLI に渡す環境変数 (PATH を解決した後のもの)
	Env []string
	// Timeout は CLI の 1 回の呼び出しの上限
	Timeout time.Duration
}

// Setting は workflow 定義のうち、部品の組み立てに要るもの。
type Setting struct {
	Place    Place
	Triggers []trigger.Trigger
}

// IssueStore は issue 置き場の部品 (system.md §13)。失敗は *target.Failure。一覧は、補助の要求だけが落ちたときに
// *target.NonFatal を一覧とともに返しうる (受け手は一覧で処理を続ける)。
type IssueStore interface {
	// ScopeKey は issue 置き場を指す scope key
	ScopeKey() string
	// OpenIssues は open な issue を全件読む
	OpenIssues() ([]target.Item, error)
	// ReadIssue は issue 1 件を読み直す。終端になっているか消えていれば Terminal
	ReadIssue(number int) (target.Item, error)
}

// CLStore は CL 置き場の部品 (system.md §13)。失敗は *target.Failure。一覧は、補助の要求だけが落ちたときに
// *target.NonFatal を一覧とともに返しうる (受け手は一覧で処理を続ける)。
type CLStore interface {
	// OpenCLs は open な CL を全件読む
	OpenCLs() ([]target.Item, error)
	// ReadCL は CL 1 件を読み直す。終端になっているか消えていれば Terminal
	ReadCL(number int) (target.Item, error)
}

// Parts は provider が組み立てた部品。
type Parts struct {
	Issues IssueStore
	// CLs は CL 置き場の部品。CL 置き場の役割を持たない種類は nil
	CLs CLStore
	// Confirm は起動時の issue 置き場の確認 (formats.md §6)。確認の手順を持たない種類は nil。失敗は *target.Failure か
	// *target.UnknownNames
	Confirm func() error
}

// Setup は setup が雛形を書くための宣言。
type Setup struct {
	// Flags は `setup --kind <種類>` で置き場を受けるときの flag の綴りの案内。"" なら flag で受けない
	Flags string
	// OwnsOrigin は cwd の clone の origin の host を、この種類の置き場と認めるか。nil なら認めない (どの provider も認めない
	// host は originFallback の種類と読む)
	OwnsOrigin func(host string) bool
	// CurrentPlace は cwd の clone の置き場を CLI で引く
	CurrentPlace func(Runtime) (Place, error)
	// Template は issue 置き場を埋めた雛形。cl は cwd の clone の origin の CL 置き場で、CL 置き場の役割を持たない種類だけが
	// cl: に書く (持つ種類の CL 置き場は issue 置き場と同じ場所)
	Template func(issues Place, cl scaffold.CLHost) string
}

// TakesFlags は `setup --kind <種類>` の flag で置き場を受けるか。
func (s Setup) TakesFlags() bool { return s.Flags != "" }

// ForOrigin は origin の host を認める provider。認める provider が無ければ originFallback の provider。
func ForOrigin(host string) Provider {
	for _, p := range providers {
		if p.Setup.OwnsOrigin != nil && p.Setup.OwnsOrigin(host) {
			return p
		}
	}
	p, _ := Of(originFallback)
	return p
}

// SetupFlagKinds は `setup --kind` で受ける種類 (表の順)。
func SetupFlagKinds() []Kind {
	var kinds []Kind
	for _, p := range providers {
		if p.Setup.TakesFlags() {
			kinds = append(kinds, p.Kind)
		}
	}
	return kinds
}
