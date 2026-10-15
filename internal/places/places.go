// Package places は、検査に通った workflow 定義から project の issue 置き場と CL 置き場の組を作る (system.md §13)。
// 部品の組み立て・tracker.token の渡し先・起動時の確認・撃つ CLI の一覧・scope key を 1 か所に置き、loop・試運転・doctor・
// status・paths が同じ組を使う。
package places

import (
	"errors"
	"slices"

	"github.com/swat9013/claude-dispatcher/internal/loop"
	"github.com/swat9013/claude-dispatcher/internal/provider"
	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// ScopeKey は workflow 定義の issue 置き場の scope key。置き場の部品を組まず、CLI も撃たない。
func ScopeKey(def workflow.Definition) string {
	return def.Tracker.Provider().ScopeKey(def.Tracker.Place)
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

// Store は loop が読む置き場。issue 置き場の部品は tracker.kind の provider が tracker.token を置いた環境で組み、CL 置き場の
// 部品は cl: があれば cl.kind の provider が tracker.token を置かない環境で組む (ADR 0014。cl: は CL 置き場の CLI 自身の
// 認証を使う)。cl: が無ければ issue 置き場と同じ provider の CL 置き場の部品。
func (p Places) Store() loop.Store {
	parts := p.trackerParts()
	cls := parts.CLs
	if separate, ok := p.separateCLs(); ok {
		cls = separate
	}
	return loop.Store{Issues: parts.Issues, CLs: cls}
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

// Confirm は起動時の置き場の確認 (formats.md §6): ConfirmIssues の後に ConfirmCLs。CL 置き場の補助の要求だけの失敗
// (*target.NonFatal) は、一覧を読めているので確認を落とさない。
func (p Places) Confirm() error {
	if err := p.ConfirmIssues(); err != nil {
		return err
	}
	err := p.ConfirmCLs()
	var nonFatal *target.NonFatal
	if errors.As(err, &nonFatal) {
		return nil
	}
	return err
}

// ConfirmIssues は起動時の issue 置き場の確認 (formats.md §6)。確認の手順を持たない種類は何もしない。失敗は
// *target.Failure か *target.UnknownNames。
func (p Places) ConfirmIssues() error {
	if confirm := p.trackerParts().Confirm; confirm != nil {
		return confirm()
	}
	return nil
}

// ConfirmCLs は起動時の cl: の CL 置き場の確認 (formats.md §6): open な一覧を 1 回読む。cl: が無ければ何もしない。補助の
// 要求だけが落ちれば *target.NonFatal を返す。
func (p Places) ConfirmCLs() error {
	cls, ok := p.separateCLs()
	if !ok {
		return nil
	}
	_, err := cls.OpenCLs()
	return err
}

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
// CL 側の trigger があるときだけ入れる (claim の workspace の branch を読む)。worker と hooks が撃つ git は loop の要件に
// しない。
func RequiredCommands(def workflow.Definition) []string {
	commands := append(placeCommands(def), def.Claude.Command)
	if slices.Contains(def.Kinds(), target.KindCL) {
		commands = append(commands, "git")
	}
	return commands
}
