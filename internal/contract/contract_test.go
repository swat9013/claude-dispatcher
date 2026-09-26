package contract_test

import (
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

func TestConditionCatalogMatchesTheDesignDoc(t *testing.T) {
	system := read(t, "../../docs/design/system.md")
	text := section(t, system, "## 6. 指示カタログ")

	var got []string
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z]+)` \\| .* \\| `playbook-").FindAllStringSubmatch(text, -1) {
		got = append(got, m[1])
	}

	if want := catalogNames(); !slices.Equal(got, want) {
		t.Fatalf("system.md §6 の条件カタログ = %v, CLI の条件カタログ = %v", got, want)
	}
}

func TestPromptLeavesNoPlaceholder(t *testing.T) {
	prompt, err := contract.Prompt(contract.Params{
		InstructionFile: "/s/instructions/x.json", DecisionsFile: "/s/decisions/x.json", HandoffFile: "/s/decisions/x.handoff-<N>.md",
		IssueRepo: "acme/widgets", CLRepo: "acme/widgets", ReadyLabel: "ready-for-agent", PrincipleIndex: "/p/principle-index/SKILL.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/s/instructions/x.json", "/s/decisions/x.json", "/p/principle-index/SKILL.md", "label を付けない"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt に %q が無い", want)
		}
	}
}
