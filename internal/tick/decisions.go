package tick

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
)

// Decisions は orchestrator が書く決定ファイル (formats.md §5.2)。
type Decisions struct {
	// Raw は decisions の原文。orchestrator 行へそのまま写す
	Raw       json.RawMessage
	Decisions []Decision
	Spawn     []Spawn
}

type Decision struct {
	Issue  int    `json:"issue"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

type Spawn struct {
	Issue     int      `json:"issue"`
	Kind      string   `json:"kind"`
	Prompt    string   `json:"prompt"`
	Playbooks []string `json:"playbooks"`
}

var (
	decisionActions = []string{"start", "reenter", "skip", "ready-for-human"}
	spawnKinds      = []string{"start", "reenter"}
)

// ReadDecisions は決定ファイルを読んで検査する。検査に落ちたら 1 件も起動しない (半分起動した残骸を作らない)。
// 網羅の検査は起動の後に行うので、ここでは扱わない (CoverageGap)。
func ReadDecisions(file string, instructions []Instruction) (Decisions, error) {
	raw, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return Decisions{}, fmt.Errorf("orchestrator が決定ファイルを書かなかった (%s)", file)
	}
	if err != nil {
		return Decisions{}, fmt.Errorf("決定ファイルを読めない (%s): %w", file, err)
	}
	var doc struct {
		Decisions json.RawMessage `json:"decisions"`
		Spawn     []Spawn         `json:"spawn"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Decisions{}, fmt.Errorf("決定ファイルが JSON として読めない (%s): %w", file, err)
	}
	d := Decisions{Raw: doc.Decisions, Spawn: doc.Spawn}
	if len(doc.Decisions) == 0 || doc.Spawn == nil {
		return Decisions{}, fmt.Errorf("決定ファイルに decisions と spawn の両方が要る (%s)", file)
	}
	if err := json.Unmarshal(doc.Decisions, &d.Decisions); err != nil {
		return Decisions{}, fmt.Errorf("決定ファイルの decisions が読めない (%s): %w", file, err)
	}
	if err := validate(d, instructions); err != nil {
		return Decisions{}, fmt.Errorf("決定ファイルが検査に落ちた (%s): %w", file, err)
	}
	return d, nil
}

func validate(d Decisions, instructions []Instruction) error {
	actionOf := map[int]string{}
	for _, entry := range d.Decisions {
		if !slices.Contains(decisionActions, entry.Action) {
			return fmt.Errorf("issue %d の action %q は %s のいずれかでない", entry.Issue, entry.Action, strings.Join(decisionActions, " / "))
		}
		actionOf[entry.Issue] = entry.Action
	}

	// reenter の worker へ渡してよい playbook は、指示に載せた条件の playbook を条件の順に並べたもの
	reenterPlaybooks := map[int][]string{}
	// start の worker へ渡してよい playbook は、指示に載せた選定母集合の 1 本
	var startPlaybooks []string
	for _, i := range instructions {
		switch i := i.(type) {
		case ReenterInstruction:
			for _, c := range i.Conditions {
				reenterPlaybooks[i.Issue] = append(reenterPlaybooks[i.Issue], c.Playbook)
			}
		case StartInstruction:
			for _, p := range i.Playbooks {
				startPlaybooks = append(startPlaybooks, p.Path)
			}
		}
	}

	for _, s := range d.Spawn {
		if !slices.Contains(spawnKinds, s.Kind) {
			return fmt.Errorf("spawn (issue %d) の kind %q は %s のいずれかでない", s.Issue, s.Kind, strings.Join(spawnKinds, " / "))
		}
		if actionOf[s.Issue] != s.Kind {
			return fmt.Errorf("spawn (issue %d, kind %s) に同じ action の decision が無い", s.Issue, s.Kind)
		}
		if s.Prompt == "" {
			return fmt.Errorf("spawn (issue %d) の prompt が空", s.Issue)
		}
		if strings.Contains(s.Prompt, "${") {
			// 未展開の変数は worker から解決できず、playbook も索引も届かないまま走る
			return fmt.Errorf("spawn (issue %d) の prompt に未展開の変数が残っている", s.Issue)
		}
		if len(s.Playbooks) == 0 {
			return fmt.Errorf("spawn (issue %d) の playbooks が空", s.Issue)
		}
		for _, p := range s.Playbooks {
			// worker が読むのは prompt の path だけ。列挙と prompt の食い違い・消えた playbook は起動前に落とす
			if p == "" || !strings.Contains(s.Prompt, p) {
				return fmt.Errorf("spawn (issue %d) の playbook が prompt に載っていない: %s", s.Issue, p)
			}
			if info, err := os.Stat(p); err != nil || info.IsDir() {
				return fmt.Errorf("spawn (issue %d) の playbook が実在しない: %s", s.Issue, p)
			}
		}
		switch s.Kind {
		case "start":
			if len(s.Playbooks) != 1 || !slices.Contains(startPlaybooks, s.Playbooks[0]) {
				return fmt.Errorf("start の spawn (issue %d) の playbooks が指示の選定母集合の 1 本でない: %v", s.Issue, s.Playbooks)
			}
		case "reenter":
			// 読み直しで外れた条件は落としてよいが、指示に無い playbook・順序の入れ替えは写し間違い
			if !isSubsequence(s.Playbooks, reenterPlaybooks[s.Issue]) {
				return fmt.Errorf("reenter の spawn (issue %d) の playbooks が指示の条件の playbook を条件の順に並べたものでない: %v", s.Issue, s.Playbooks)
			}
		}
	}
	return nil
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

// CoverageGap は指示ごとに採否が書かれているかを見て、欠けを 1 文で返す (無ければ "")。
// 判断不能を orchestrator が黙って落とせないようにする検査。
func CoverageGap(d Decisions, instructions []Instruction) string {
	actionOf := map[int]string{}
	for _, entry := range d.Decisions {
		actionOf[entry.Issue] = entry.Action
	}
	var gaps []string
	for _, i := range instructions {
		var undecided []string
		for _, n := range i.DecisionIssues() {
			if !slices.Contains(i.AllowedActions(), actionOf[n]) {
				undecided = append(undecided, fmt.Sprint(n))
			}
		}
		if len(undecided) > 0 {
			gaps = append(gaps, fmt.Sprintf("%s の issue [%s] に採否 (%s) が無い",
				i.label(), strings.Join(undecided, ", "), strings.Join(i.AllowedActions(), " / ")))
		}
	}
	return strings.Join(gaps, " / ")
}
