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
	Action Action `json:"action"`
	Reason string `json:"reason"`
}

// Spawn は起動する worker 1 つ。Kind は同じ issue の採否 (start / reenter) と同じ綴り。
type Spawn struct {
	Issue     int      `json:"issue"`
	Kind      Action   `json:"kind"`
	Prompt    string   `json:"prompt"`
	Playbooks []string `json:"playbooks"`
}

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
	// decisions / spawn の欠落は 0 件として読む。採否の欠けは網羅の検査が拾う
	d := Decisions{Raw: doc.Decisions, Spawn: doc.Spawn}
	if len(doc.Decisions) > 0 {
		if err := json.Unmarshal(doc.Decisions, &d.Decisions); err != nil {
			return Decisions{}, fmt.Errorf("決定ファイルの decisions が読めない (%s): %w", file, err)
		}
	}
	if err := validate(d, instructions); err != nil {
		return Decisions{}, fmt.Errorf("決定ファイルが検査に落ちた (%s): %w", file, err)
	}
	return d, nil
}

// validate は formats.md §5.2 の起動前の検査。採否はその issue の指示が許す語彙に入っていなければならない
// (どの指示にも無い issue への採否は、指示の外で worker を起動する経路になる)。
func validate(d Decisions, instructions []Instruction) error {
	instructionsOf := map[int][]Instruction{}
	for _, i := range instructions {
		for _, n := range i.DecisionIssues() {
			instructionsOf[n] = append(instructionsOf[n], i)
		}
	}
	// allowing は issue の指示のうち action を許すもの。無ければ nil
	allowing := func(issue int, action Action) Instruction {
		for _, i := range instructionsOf[issue] {
			if slices.Contains(i.AllowedActions(), action) {
				return i
			}
		}
		return nil
	}

	actionOf := map[int]Action{}
	for _, entry := range d.Decisions {
		if allowing(entry.Issue, entry.Action) == nil {
			return fmt.Errorf("issue %d の action %q はその issue の指示が許す採否でない (%s)", entry.Issue, entry.Action, allowedText(instructionsOf[entry.Issue]))
		}
		actionOf[entry.Issue] = entry.Action
	}

	for _, s := range d.Spawn {
		if s.Kind != ActionStart && s.Kind != ActionReenter {
			return fmt.Errorf("spawn (issue %d) の kind %q は start / reenter のいずれかでない", s.Issue, s.Kind)
		}
		if actionOf[s.Issue] != s.Kind {
			return fmt.Errorf("spawn (issue %d, kind %s) に同じ action の decision が無い", s.Issue, s.Kind)
		}
		if strings.Contains(s.Prompt, "${") {
			// 未展開の変数は worker から解決できず、playbook も索引も届かないまま走る
			return fmt.Errorf("spawn (issue %d) の prompt に未展開の変数が残っている", s.Issue)
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
		if err := allowing(s.Issue, s.Kind).checkSpawnPlaybooks(s.Playbooks); err != nil {
			return fmt.Errorf("spawn (issue %d): %w", s.Issue, err)
		}
	}
	return validateSpawnList(d.Spawn, instructions)
}

// validateSpawnList は spawn の列全体に掛かる検査。start の件数は kind だけで数える
// (start 指示の無い tick の start spawn は、1 件ずつの検査が採否の語彙で弾く)。
func validateSpawnList(spawns []Spawn, instructions []Instruction) error {
	seen := map[int]bool{}
	for _, s := range spawns {
		if seen[s.Issue] {
			// worker log (workers/<issue>-<stem>.log) と worktree-issue-<N> を 2 つの worker が取り合う
			return fmt.Errorf("spawn に issue %d が 2 件ある", s.Issue)
		}
		seen[s.Issue] = true
	}
	starts := 0
	for _, s := range spawns {
		if s.Kind == ActionStart {
			starts++
		}
	}
	for _, i := range instructions {
		if start, ok := i.(StartInstruction); ok && starts > start.FreeSlots {
			// 並列上限 N を orchestrator の判断だけに任せない (起動した後の wip_over_limit では遅い)
			return fmt.Errorf("start の spawn が %d 件あり、指示の free_slots (%d) を超える", starts, start.FreeSlots)
		}
	}
	return nil
}

func allowedText(instructions []Instruction) string {
	if len(instructions) == 0 {
		return "どの指示にも無い issue"
	}
	var parts []string
	for _, i := range instructions {
		parts = append(parts, fmt.Sprintf("%s: %s", i.label(), joinActions(i.AllowedActions())))
	}
	return strings.Join(parts, "; ")
}

func joinActions(actions []Action) string {
	names := make([]string, 0, len(actions))
	for _, a := range actions {
		names = append(names, string(a))
	}
	return strings.Join(names, " / ")
}

// CoverageGap は指示ごとに採否が書かれているかを見て、欠けを 1 文で返す (無ければ "")。
// 判断不能を orchestrator が黙って落とせないようにする検査。
func CoverageGap(d Decisions, instructions []Instruction) string {
	actionOf := map[int]Action{}
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
				i.label(), strings.Join(undecided, ", "), joinActions(i.AllowedActions())))
		}
	}
	return strings.Join(gaps, " / ")
}
