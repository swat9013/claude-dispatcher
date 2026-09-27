package tick

import (
	"fmt"
	"maps"
	"slices"

	"github.com/swat9013/claude-dispatcher/internal/plugin"
)

// Condition は reenter の条件 1 つ: snapshot の CL 状態に対する述語と、worker へ渡す対応 playbook。
type Condition struct {
	Name     string
	Holds    func(CL) bool
	Playbook string // plugin の procedure 下の playbook 名
}

// Conditions は条件カタログ (system.md §6)。条件名・述語・対応 playbook の定義元はここだけで、
// 並びが指示の `conditions` の順になる。契約 file の「条件ごとの読み直し方法」と並びが一致することをテストが検査する。
var Conditions = []Condition{
	{"conflict", func(cl CL) bool { return cl.Mergeable == "CONFLICTING" }, "playbook-conflict-resolution"},
	{"review", func(cl CL) bool { return cl.UnresolvedThreads > 0 }, "playbook-review-response"},
	{"ci", func(cl CL) bool { return cl.Checks != nil && (*cl.Checks == "FAILURE" || *cl.Checks == "ERROR") }, "playbook-ci-fix"},
}

// Action は決定ファイルの採否の語彙 (formats.md §5.2)。起動する採否 (start / reenter) は spawn の kind と同じ綴り。
type Action string

const (
	ActionStart         Action = "start"
	ActionReenter       Action = "reenter"
	ActionSkip          Action = "skip"
	ActionReadyForHuman Action = "ready-for-human"
)

// AnomalyReason は機械的に分類できなかった観測の種類
type AnomalyReason string

const (
	ReasonMultipleOpenCLs     AnomalyReason = "multiple_open_cls"
	ReasonWIPOverLimit        AnomalyReason = "wip_over_limit"
	ReasonWIPAndReadyForHuman AnomalyReason = "wip_and_ready_for_human"
)

// Instruction は指示 1 つ。orchestrator の採否を検査するための面を持つ。
type Instruction interface {
	Kind() string
	// DecisionIssues は採否が要る issue
	DecisionIssues() []int
	// AllowedActions はその issue に許される採否
	AllowedActions() []Action
	label() string
	// withPlaybooks は指示に添える playbook を plugin の install 先の絶対 path で埋めた指示を返す
	withPlaybooks(plugin.Install) (Instruction, error)
	// checkSpawnPlaybooks は spawn に載った playbook の列がこの指示から渡してよいものかを検査する
	checkSpawnPlaybooks([]string) error
}

// StartPlaybook は start 指示に添える選定母集合の 1 本 (指示ファイルの形)
type StartPlaybook struct {
	Path         string `json:"path"`
	DispatchWhen string `json:"dispatch_when"`
}

type StartInstruction struct {
	FreeSlots  int             `json:"free_slots"`
	Candidates []Candidate     `json:"candidates"`
	Playbooks  []StartPlaybook `json:"playbooks"`
}

func (StartInstruction) Kind() string             { return "start" }
func (StartInstruction) AllowedActions() []Action { return []Action{ActionStart, ActionSkip} }
func (i StartInstruction) label() string          { return i.Kind() }
func (i StartInstruction) MarshalJSON() ([]byte, error) {
	type plain StartInstruction
	return marshalCompact(struct {
		Kind string `json:"kind"`
		plain
	}{i.Kind(), plain(i)})
}
func (i StartInstruction) DecisionIssues() []int {
	out := make([]int, 0, len(i.Candidates))
	for _, c := range i.Candidates {
		out = append(out, c.Number)
	}
	return out
}

func (i StartInstruction) withPlaybooks(install plugin.Install) (Instruction, error) {
	found, err := install.StartPlaybooks()
	if err != nil {
		return nil, err
	}
	i.Playbooks = make([]StartPlaybook, 0, len(found))
	for _, p := range found {
		i.Playbooks = append(i.Playbooks, StartPlaybook{Path: p.Path, DispatchWhen: p.DispatchWhen})
	}
	return i, nil
}

// start の worker へ渡してよい playbook は、指示に載せた選定母集合の 1 本
func (i StartInstruction) checkSpawnPlaybooks(playbooks []string) error {
	if len(playbooks) == 1 && slices.ContainsFunc(i.Playbooks, func(p StartPlaybook) bool { return p.Path == playbooks[0] }) {
		return nil
	}
	return fmt.Errorf("start の playbooks が指示の選定母集合の 1 本でない: %v", playbooks)
}

type ReenterInstruction struct {
	Issue      int                `json:"issue"`
	CL         CLBrief            `json:"cl"`
	Conditions []ConditionPointer `json:"conditions"`
}

// CLBrief は再入 worker が要る分だけの CL の像
type CLBrief struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Branch string `json:"branch"`
	Base   string `json:"base"`
}

// ConditionPointer は立っている条件 1 つと、worker へ渡す対応 playbook (Derive の直後は名前、WithPlaybooks の後は絶対 path)
type ConditionPointer struct {
	Name     string `json:"name"`
	Playbook string `json:"playbook"`
}

func (ReenterInstruction) Kind() string             { return "reenter" }
func (ReenterInstruction) AllowedActions() []Action { return []Action{ActionReenter, ActionSkip} }
func (i ReenterInstruction) label() string          { return i.Kind() }
func (i ReenterInstruction) DecisionIssues() []int  { return []int{i.Issue} }
func (i ReenterInstruction) MarshalJSON() ([]byte, error) {
	type plain ReenterInstruction
	return marshalCompact(struct {
		Kind string `json:"kind"`
		plain
	}{i.Kind(), plain(i)})
}

func (i ReenterInstruction) withPlaybooks(install plugin.Install) (Instruction, error) {
	conditions := make([]ConditionPointer, 0, len(i.Conditions))
	for _, c := range i.Conditions {
		conditions = append(conditions, ConditionPointer{Name: c.Name, Playbook: install.Playbook(c.Playbook)})
	}
	i.Conditions = conditions
	return i, nil
}

// reenter の worker へ渡してよい playbook は、指示に載せた条件の playbook を条件の順に並べた列の部分列。
// 読み直しで外れた条件は落としてよいが、指示に無い playbook・順序の入れ替えは写し間違い
func (i ReenterInstruction) checkSpawnPlaybooks(playbooks []string) error {
	var allowed []string
	for _, c := range i.Conditions {
		allowed = append(allowed, c.Playbook)
	}
	if isSubsequence(playbooks, allowed) {
		return nil
	}
	return fmt.Errorf("reenter の playbooks が指示の条件の playbook を条件の順に並べたものでない: %v", playbooks)
}

type AnomalyInstruction struct {
	Reason AnomalyReason `json:"reason"`
	Issues []int         `json:"issues"`
	CLs    []int         `json:"cls,omitempty"` // multiple_open_cls だけが持つ
}

func (AnomalyInstruction) Kind() string             { return "anomaly" }
func (AnomalyInstruction) AllowedActions() []Action { return []Action{ActionSkip, ActionReadyForHuman} }
func (i AnomalyInstruction) label() string          { return "anomaly (" + string(i.Reason) + ")" }
func (i AnomalyInstruction) DecisionIssues() []int  { return i.Issues }
func (i AnomalyInstruction) MarshalJSON() ([]byte, error) {
	type plain AnomalyInstruction
	return marshalCompact(struct {
		Kind string `json:"kind"`
		plain
	}{i.Kind(), plain(i)})
}
func (i AnomalyInstruction) withPlaybooks(plugin.Install) (Instruction, error) { return i, nil }

// anomaly の採否 (skip / ready-for-human) は worker を起動しない
func (i AnomalyInstruction) checkSpawnPlaybooks([]string) error {
	return fmt.Errorf("%s は worker を起動しない", i.label())
}

func isSubsequence(sub, of []string) bool {
	i := 0
	for _, x := range of {
		if i < len(sub) && sub[i] == x {
			i++
		}
	}
	return i == len(sub)
}

// WithPlaybooks は指示に添える playbook を install 先の絶対 path で埋める。
func WithPlaybooks(instructions []Instruction, install plugin.Install) ([]Instruction, error) {
	out := make([]Instruction, 0, len(instructions))
	for _, i := range instructions {
		resolved, err := i.withPlaybooks(install)
		if err != nil {
			return nil, err
		}
		out = append(out, resolved)
	}
	return out, nil
}

// Derive は snapshot から機械的に確定する指示だけを出す。分類できない観測は anomaly として上げる。
// 並びは reenter → start → anomaly。slot は reenter → start の順に配る。
// playbook は名前 (条件名) までしか埋めない。plugin の解決は指示があるときだけ WithPlaybooks が行う。
func Derive(s Snapshot) []Instruction {
	wip := map[int]bool{}
	for _, i := range s.Issues.WIP {
		wip[i.Number] = true
	}
	human := map[int]bool{}
	for _, n := range s.Issues.ReadyForHuman {
		human[n] = true
	}
	clByNumber := map[int]CL{}
	for _, cl := range s.CLs {
		clByNumber[cl.Number] = cl
	}
	freeSlots := s.Limits.MaxWIP - len(s.Issues.WIP)

	var reenters, anomalies []Instruction
	for _, issue := range slices.Sorted(maps.Keys(s.LinkedCLs)) {
		numbers := s.LinkedCLs[issue]
		if len(numbers) > 1 {
			cls := slices.Clone(numbers)
			slices.Sort(cls)
			anomalies = append(anomalies, AnomalyInstruction{Reason: ReasonMultipleOpenCLs, Issues: []int{issue}, CLs: cls})
			continue
		}
		cl := clByNumber[numbers[0]]
		// 人が開いた CL (fork の CL を含む) には再入しない。wip / ready-for-human の付いた issue にも出さない (system.md §6)
		if wip[issue] || human[issue] || !isWorkerCL(cl.Branch, cl.Fork, issue) {
			continue
		}
		var conditions []ConditionPointer
		for _, c := range Conditions {
			if c.Holds(cl) {
				// playbook は名前で置き、WithPlaybooks が install 先の絶対 path に置き換える
				conditions = append(conditions, ConditionPointer{Name: c.Name, Playbook: c.Playbook})
			}
		}
		if len(conditions) > 0 {
			reenters = append(reenters, ReenterInstruction{
				Issue: issue, Conditions: conditions,
				CL: CLBrief{Number: cl.Number, URL: cl.URL, Branch: cl.Branch, Base: cl.Base},
			})
		}
	}

	instructions := reenters[:min(len(reenters), max(freeSlots, 0))]
	freeSlots -= len(instructions)

	if len(s.Issues.Candidates) > 0 && freeSlots > 0 {
		instructions = append(instructions, StartInstruction{FreeSlots: freeSlots, Candidates: s.Issues.Candidates})
	}

	if len(s.Issues.WIP) > s.Limits.MaxWIP {
		numbers := make([]int, 0, len(s.Issues.WIP))
		for _, i := range s.Issues.WIP {
			numbers = append(numbers, i.Number)
		}
		instructions = append(instructions, AnomalyInstruction{Reason: ReasonWIPOverLimit, Issues: numbers})
	}
	var both []int
	for n := range wip {
		if human[n] {
			both = append(both, n)
		}
	}
	if len(both) > 0 {
		slices.Sort(both)
		instructions = append(instructions, AnomalyInstruction{Reason: ReasonWIPAndReadyForHuman, Issues: both})
	}
	return append(instructions, anomalies...)
}

// Counts は指示の種別 → 件数 (log.jsonl の `instructions`)。
func Counts(instructions []Instruction) map[string]int {
	out := map[string]int{}
	for _, i := range instructions {
		out[i.Kind()]++
	}
	return out
}
