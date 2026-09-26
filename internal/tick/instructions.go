package tick

import (
	"slices"
	"strconv"

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

// Instruction は指示 1 つ。orchestrator の採否の網羅を検査するための面を持つ。
type Instruction interface {
	Kind() string
	// DecisionIssues は採否が要る issue
	DecisionIssues() []int
	// AllowedActions はその issue に許される採否
	AllowedActions() []string
	label() string
}

type StartInstruction struct {
	KindName   string                 `json:"kind"`
	FreeSlots  int                    `json:"free_slots"`
	Candidates []Candidate            `json:"candidates"`
	Playbooks  []plugin.StartPlaybook `json:"playbooks"`
}

func (StartInstruction) Kind() string             { return "start" }
func (StartInstruction) AllowedActions() []string { return []string{"start", "skip"} }
func (i StartInstruction) label() string          { return "start" }
func (i StartInstruction) DecisionIssues() []int {
	out := make([]int, 0, len(i.Candidates))
	for _, c := range i.Candidates {
		out = append(out, c.Number)
	}
	return out
}

type ReenterInstruction struct {
	KindName   string             `json:"kind"`
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

type ConditionPointer struct {
	Name     string `json:"name"`
	Playbook string `json:"playbook"`
}

func (ReenterInstruction) Kind() string             { return "reenter" }
func (ReenterInstruction) AllowedActions() []string { return []string{"reenter", "skip"} }
func (i ReenterInstruction) label() string          { return "reenter" }
func (i ReenterInstruction) DecisionIssues() []int  { return []int{i.Issue} }

type AnomalyInstruction struct {
	KindName string `json:"kind"`
	Reason   string `json:"reason"`
	Issues   []int  `json:"issues"`
	CLs      []int  `json:"cls,omitempty"` // multiple_open_cls だけが持つ
}

func (AnomalyInstruction) Kind() string             { return "anomaly" }
func (AnomalyInstruction) AllowedActions() []string { return []string{"skip", "ready-for-human"} }
func (i AnomalyInstruction) label() string          { return "anomaly (" + i.Reason + ")" }
func (i AnomalyInstruction) DecisionIssues() []int  { return i.Issues }

// PlaybookSource は指示に添える playbook の絶対 path を返す。plugin の解決は指示が要るときだけ行う。
type PlaybookSource interface {
	Playbook(name string) (string, error)
	StartPlaybooks() ([]plugin.StartPlaybook, error)
}

// Derive は snapshot から機械的に確定する指示だけを出す。分類できない観測は anomaly として上げる。
// 並びは reenter → start → anomaly。slot は reenter → start の順に配る。
func Derive(s Snapshot, playbooks PlaybookSource) ([]Instruction, error) {
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
	for _, issue := range sortedIssues(s.LinkedCLs) {
		numbers := s.LinkedCLs[strconv.Itoa(issue)]
		if len(numbers) > 1 {
			cls := slices.Clone(numbers)
			slices.Sort(cls)
			anomalies = append(anomalies, AnomalyInstruction{KindName: "anomaly", Reason: "multiple_open_cls", Issues: []int{issue}, CLs: cls})
			continue
		}
		cl := clByNumber[numbers[0]]
		// 人が開いた CL には再入しない。wip / ready-for-human の付いた issue にも出さない (system.md §6)
		if wip[issue] || human[issue] || cl.Branch != WorkerBranch(issue) {
			continue
		}
		var conditions []ConditionPointer
		for _, c := range Conditions {
			if !c.Holds(cl) {
				continue
			}
			path, err := playbooks.Playbook(c.Playbook)
			if err != nil {
				return nil, err
			}
			conditions = append(conditions, ConditionPointer{Name: c.Name, Playbook: path})
		}
		if len(conditions) > 0 {
			reenters = append(reenters, ReenterInstruction{
				KindName: "reenter", Issue: issue, Conditions: conditions,
				CL: CLBrief{Number: cl.Number, URL: cl.URL, Branch: cl.Branch, Base: cl.Base},
			})
		}
	}

	instructions := reenters[:min(len(reenters), max(freeSlots, 0))]
	freeSlots -= len(instructions)

	if len(s.Issues.Candidates) > 0 && freeSlots > 0 {
		starts, err := playbooks.StartPlaybooks()
		if err != nil {
			return nil, err
		}
		instructions = append(instructions, StartInstruction{KindName: "start", FreeSlots: freeSlots, Candidates: s.Issues.Candidates, Playbooks: starts})
	}

	if len(s.Issues.WIP) > s.Limits.MaxWIP {
		numbers := make([]int, 0, len(s.Issues.WIP))
		for _, i := range s.Issues.WIP {
			numbers = append(numbers, i.Number)
		}
		instructions = append(instructions, AnomalyInstruction{KindName: "anomaly", Reason: "wip_over_limit", Issues: numbers})
	}
	var both []int
	for n := range wip {
		if human[n] {
			both = append(both, n)
		}
	}
	if len(both) > 0 {
		slices.Sort(both)
		instructions = append(instructions, AnomalyInstruction{KindName: "anomaly", Reason: "wip_and_ready_for_human", Issues: both})
	}
	return append(instructions, anomalies...), nil
}

func sortedIssues(linked map[string][]int) []int {
	out := make([]int, 0, len(linked))
	for key := range linked {
		n, _ := strconv.Atoi(key)
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// Counts は指示の種別 → 件数 (log.jsonl の `instructions`)。
func Counts(instructions []Instruction) map[string]int {
	out := map[string]int{}
	for _, i := range instructions {
		out[i.Kind()]++
	}
	return out
}
