package contract_test

import (
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/contract"
	"github.com/swat9013/claude-dispatcher/internal/tick"
)

// 決定ファイルの形と条件カタログは、埋め込んだ契約 (orchestrator が読む) と設計 doc / CLI の両方が持つ。
// 食い違うと黙って壊れるので、ここで一致を検査する (ADR 0002 / 0003)。

func read(t *testing.T, file string) string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// section は markdown の見出し heading から次の同じ深さ以上の見出しまでを返す。
func section(t *testing.T, doc, heading string) string {
	t.Helper()
	start := strings.Index(doc, heading+"\n")
	if start < 0 {
		t.Fatalf("見出し %q が無い", heading)
	}
	level := strings.Count(strings.Fields(heading)[0], "#")
	rest := doc[start+len(heading):]
	next := regexp.MustCompile(`\n#{1,` + string(rune('0'+level)) + `} `).FindStringIndex(rest)
	if next == nil {
		return rest
	}
	return rest[:next[0]]
}

func firstJSONBlock(t *testing.T, text string) string {
	t.Helper()
	m := regexp.MustCompile("(?s)```json\n(.*?)```").FindStringSubmatch(text)
	if m == nil {
		t.Fatal("json の code block が無い")
	}
	return m[1]
}

func TestContractCarriesTheDecisionsFileExampleOfFormats(t *testing.T) {
	formats := read(t, "../../docs/design/formats.md")

	want := firstJSONBlock(t, section(t, formats, "### 5.2 決定ファイル (`decisions/<stem>.json`)"))

	if got := firstJSONBlock(t, section(t, contract.Text(), "### 決定ファイルの形")); got != want {
		t.Fatalf("契約の決定ファイルの例が formats.md §5.2 と違う:\n契約:\n%s\nformats.md:\n%s", got, want)
	}
}

func catalogNames() []string {
	var names []string
	for _, c := range tick.Conditions {
		names = append(names, c.Name)
	}
	return names
}

func TestContractRereadsConditionsInCatalogOrder(t *testing.T) {
	text := section(t, contract.Text(), "### 条件ごとの読み直し方法")

	var got []string
	for _, m := range regexp.MustCompile("(?m)^- `([a-z]+)`:").FindAllStringSubmatch(text, -1) {
		got = append(got, m[1])
	}

	if want := catalogNames(); !slices.Equal(got, want) {
		t.Fatalf("契約の条件の並び = %v, 条件カタログ = %v", got, want)
	}
}

// actionTable は「| `<指示>` | … | `<action>` / `<action>` |」の行から 指示 → 許される action を読む。
func actionTable(t *testing.T, text string) map[string][]string {
	t.Helper()
	table := map[string][]string{}
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z]+)` \\| [^|]+ \\| ([^|]+) \\|$").FindAllStringSubmatch(text, -1) {
		for _, a := range regexp.MustCompile("`([a-z-]+)`").FindAllStringSubmatch(m[2], -1) {
			table[m[1]] = append(table[m[1]], a[1])
		}
	}
	if len(table) == 0 {
		t.Fatal("許される action の表が無い")
	}
	return table
}

func TestContractAndFormatsListTheActionsEachInstructionAllows(t *testing.T) {
	want := map[string][]string{}
	for _, i := range []tick.Instruction{tick.StartInstruction{}, tick.ReenterInstruction{}, tick.AnomalyInstruction{}} {
		for _, a := range i.AllowedActions() {
			want[i.Kind()] = append(want[i.Kind()], string(a))
		}
	}
	formats := read(t, "../../docs/design/formats.md")

	for name, text := range map[string]string{
		"契約":         section(t, contract.Text(), "### 決定ファイルの形"),
		"formats.md": section(t, formats, "### 5.2 決定ファイル (`decisions/<stem>.json`)"),
	} {
		if got := actionTable(t, text); !maps.EqualFunc(got, want, slices.Equal) {
			t.Fatalf("%s の許される action = %v, CLI = %v", name, got, want)
		}
	}
}

func params(triage string) contract.Params {
	return contract.Params{
		InstructionFile: "/s/instructions/x.json", DecisionsFile: "/s/decisions/x.json", HandoffFile: "/s/decisions/x.handoff-<N>.md",
		IssueRepo: "acme/widgets", CLRepo: "acme/cls", ReadyLabel: "ready-for-agent", TriageLabel: triage,
		PrincipleIndex: "/p/principle-index/SKILL.md",
	}
}

func TestPromptFillsEveryPlaceholder(t *testing.T) {
	prompt, err := contract.Prompt(params(""))

	if err != nil || strings.Contains(prompt, "<<") {
		t.Fatalf("差し込まれていない placeholder が残った: %v", err)
	}
	for _, want := range []string{"/s/instructions/x.json", "/s/decisions/x.json", "/s/decisions/x.handoff-<N>.md",
		"acme/widgets", "acme/cls", "ready-for-agent", "dispatcher:wip", "/p/principle-index/SKILL.md"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt に %q が無い", want)
		}
	}
}

func TestPromptTellsWorkersToFileFollowUpsWithoutALabelWhenNoTriageLabelIsDeclared(t *testing.T) {
	prompt, _ := contract.Prompt(params(""))

	if !strings.Contains(prompt, "起票には label を付けない") {
		t.Fatal("triage label が無いときの起票の規則が無い")
	}
}

func TestPromptTellsWorkersToFileFollowUpsWithTheDeclaredTriageLabel(t *testing.T) {
	prompt, _ := contract.Prompt(params("needs-triage"))

	if !strings.Contains(prompt, "起票には triage label `needs-triage` だけを付ける") {
		t.Fatal("宣言した triage label で起票する規則が無い")
	}
}
