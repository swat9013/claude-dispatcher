// Package places は、検査に通った workflow 定義から project の issue 置き場と CL 置き場の組を作る (system.md §13)。
// 部品の組み立て・tracker.token の渡し先・起動時の確認・撃つ CLI の一覧・scope key に加え、組を読む口 (Store)・置き場の名前・遅い一覧の知らせを
// 1 か所に置き、loop・試運転・doctor・status・paths が同じ組を使う。
package places

import (
	"errors"
	"slices"

	"github.com/swat9013/claude-dispatcher/internal/provider"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// ScopeKey は workflow 定義の issue 置き場の scope key。置き場の部品を組まず、CLI も撃たない。
func ScopeKey(def workflow.Definition) string {
	return def.Tracker.Provider().ScopeKey(def.Tracker.Place)
}

// Label は kind の作業対象の置き場を人が読む行で指す綴り (`issue 置き場 <置き場>` か `CL 置き場 <置き場>`)。doctor の行と
// 遅い一覧の行が使う (formats.md §6・§7.4)。
func Label(def workflow.Definition, kind target.Kind) string {
	name := "issue 置き場 "
	if kind == target.KindCL {
		name = "CL 置き場 "
	}
	return name + Place(def, kind)
}

// Place は kind の作業対象の置き場の綴り。
func Place(def workflow.Definition, kind target.Kind) string {
	if kind == target.KindCL {
		_, place := def.CLHost()
		return place.String()
	}
	return def.Tracker.Place.String()
}

// Places は workflow 定義 1 つの置き場の組。
type Places struct {
	def workflow.Definition
	// rt は tracker.token を置く前の実行環境
	rt provider.Runtime
}

// New は workflow 定義 def の置き場の組。rt.Env には tracker.token を置かない環境を渡す (置く先は Places が決める)。
func New(def workflow.Definition, rt provider.Runtime) Places {
	return Places{def: def, rt: rt}
}

// TokenEnv は rt.Env に tracker.token を、tracker の CLI が読む環境変数として置いた環境 (system.md §8)。issue 置き場の CLI と、
// hooks・worker に渡す。tracker.token が書かれていなければ rt.Env のまま。
func (p Places) TokenEnv() []string {
	return p.def.Tracker.Provider().WithToken(p.rt.Env, p.def.Tracker.Token)
}

// Store は loop・試運転・doctor が読む置き場。issue 置き場の部品は tracker.kind の provider が tracker.token を置いた環境で組み、CL 置き場の
// 部品は cl: があれば cl.kind の provider が tracker.token を置かない環境で組む (ADR 0014。cl: は CL 置き場の CLI 自身の
// 認証を使う)。cl: が無ければ issue 置き場と同じ provider の CL 置き場の部品。
func (p Places) Store() Store {
	parts := p.trackerParts()
	cls := parts.CLs
	if separate, ok := p.separateCLs(); ok {
		cls = separate
	}
	return Store{Issues: parts.Issues, CLs: cls}
}

func (p Places) trackerParts() provider.Parts {
	rt := p.rt
	rt.Env = p.TokenEnv()
	return p.def.Tracker.Provider().Build(rt, provider.Setting{Place: p.def.Tracker.Place, Triggers: p.def.Triggers})
}

// separateCLs は cl: の CL 置き場の部品。tracker.token を置かない環境で組む。cl: が無ければ false。
func (p Places) separateCLs() (provider.CLStore, bool) {
	cl, place, ok := p.def.SeparateCLHost()
	if !ok {
		return nil, false
	}
	return cl.Build(p.rt, provider.Setting{Place: place, Triggers: p.def.Triggers}).CLs, true
}

// ConfirmedStore は起動時の置き場の確認 (formats.md §6) を ConfirmIssues・ConfirmCLs の順に通し、確認で読んだ open な一覧を
// snapshot に使う置き場を返す (試運転。formats.md §5)。CL 置き場の補助の要求だけの失敗 (*target.NonFatal) は、一覧を読めて
// いるので確認を落とさず、置き場の一覧とともに返す。作業対象の読み直しは、どの部品も置き場から読む。返す置き場は、確認が
// 読まなかった種類の一覧を読むときにも、遅い一覧を slow で知らせる。
func (p Places) ConfirmedStore(slow SlowNotice) (Store, error) {
	issues, err := p.ConfirmIssues(slow)
	if err != nil {
		return Store{}, err
	}
	cls, err := p.ConfirmCLs(slow)
	var nonFatal *target.NonFatal
	if err != nil && !errors.As(err, &nonFatal) {
		return Store{}, err
	}
	return Store{Issues: issues, CLs: cls}.WithSlow(slow), nil
}

// ConfirmIssues は起動時の issue 置き場の確認 (formats.md §6) を通し、確認で読んだ open な一覧を持つ issue 置き場を返す。
// 確認の手順を持たない種類は確かめず、tracker の部品のまま返す。失敗は *target.Failure か *target.UnknownNames。確認が
// 読む open な一覧が遅ければ slow で知らせる。
func (p Places) ConfirmIssues(slow SlowNotice) (provider.IssueStore, error) {
	parts := p.trackerParts()
	if parts.Confirm == nil {
		return parts.Issues, nil
	}
	return parts.Confirm(func(read func()) { slow.Watch(target.KindIssue, read) })
}

// ConfirmCLs は起動時の cl: の CL 置き場の確認 (formats.md §6): open な一覧を 1 回読み、読んだ一覧を持つ CL 置き場を、一覧と
// ともに返った失敗と一緒に返す。補助の要求だけが落ちれば *target.NonFatal。cl: が無ければ確かめず、issue 置き場と同じ
// provider の部品を返す。一覧の読み出しが遅ければ slow で知らせる。
func (p Places) ConfirmCLs(slow SlowNotice) (provider.CLStore, error) {
	separate, ok := p.separateCLs()
	if !ok {
		return p.trackerParts().CLs, nil
	}
	var open []target.Item
	var err error
	slow.Watch(target.KindCL, func() { open, err = separate.OpenCLs() })
	return confirmedCLs{CLStore: separate, open: open, err: err}, err
}

// confirmedCLs は、CL 置き場の確認が読んだ open な一覧を持つ CL 置き場。OpenCLs は読み直さずに、一覧と補助の要求の失敗
// (*target.NonFatal) をそのまま返す。ReadCL は置き場から読み直す。
type confirmedCLs struct {
	provider.CLStore
	open []target.Item
	// err は一覧とともに返った補助の要求の失敗。無ければ nil
	err error
}

func (c confirmedCLs) OpenCLs() ([]target.Item, error) { return c.open, c.err }

// placeCommands は置き場の CLI: tracker の CLI と、cl: があれば CL 置き場の CLI。
func placeCommands(def workflow.Definition) []string {
	commands := []string{def.Tracker.Provider().Command}
	if p, _, ok := def.SeparateCLHost(); ok {
		commands = append(commands, p.Command)
	}
	return commands
}

// Commands は loop・試運転・doctor が撃ちうる依存 CLI: 置き場の CLI・claude.command・git。PATH の解決に使う。git は
// CL 側の trigger が無くても入れる。loop の PATH を継ぐ worker と hooks が撃つため。
func Commands(def workflow.Definition) []string {
	return append(placeCommands(def), def.Claude.Command, "git")
}

// RequiredCommands は loop が自分で撃つ依存 CLI: 置き場の CLI と claude.command。起動時に解決できることを確かめる。git は
// CL 側の trigger があるとき (claim の workspace の branch を読む) と、調停 remote を指したとき (formats.md §12) だけ入れる。
// worker と hooks が撃つ git は loop の要件にしない。
func RequiredCommands(def workflow.Definition) []string {
	commands := append(placeCommands(def), def.Claude.Command)
	if slices.Contains(def.Kinds(), target.KindCL) || def.CoordinationRemote != "" {
		commands = append(commands, "git")
	}
	return commands
}
