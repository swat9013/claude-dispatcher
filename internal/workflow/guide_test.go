package workflow

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/guide"
	"github.com/swat9013/claude-dispatcher/internal/trigger"
	"gopkg.in/yaml.v3"
)

// keyTable は対応表の key の表と、値が対応表の key ごとの、その表と案内での path の綴り。
type keyTable struct {
	fields map[string]field
	nested map[string]nestedTable
}

type nestedTable struct {
	path  string
	table keyTable
}

// section は案内の `### <heading>` の節の本文 (同じ深さか浅い次の見出しの前まで)。節が無ければ false。
func section(text, heading string) (string, bool) {
	marker := "\n### " + heading + "\n"
	start := strings.Index(text, marker)
	if start < 0 {
		return "", false
	}
	body := text[start+len(marker):]
	for _, next := range []string{"\n### ", "\n## "} {
		if end := strings.Index(body, next); end >= 0 {
			body = body[:end]
		}
	}
	return body, true
}

// provider の対応表の始まりの目印。述語の節は述語の表の後に provider の対応表を、「項目」の節は key の表の後に
// 置き場のサービスごとの違いの表を持つ。
const (
	serviceDifferenceTableMarker = "**置き場のサービスごとの違い**"
	jiraProviderTableMarker      = "`tracker.kind` が `jira` のときの述語の意味:"
	gitlabProviderTableMarker    = "上の表は `github` の field で書いた。"
)

// keyCells は本文の表の行ごとの最初のセル (key の列) を返す。案内は key を表の行で説明するので、key の列だけを探し、
// 箇条書きや説明のセルに出る綴りで、説明の行が消えたのを見逃さない。
func keyCells(body string) []string {
	var cells []string
	for _, line := range strings.Split(body, "\n") {
		if cell, ok := strings.CutPrefix(line, "|"); ok {
			cells = append(cells, strings.SplitN(cell, "|", 2)[0])
		}
	}
	return cells
}

// leaves は表の key を、値が対応表でない key まで辿って、案内での path の綴りで返す。
func (k keyTable) leaves(prefix string) []string {
	var out []string
	for name := range k.fields {
		if child, ok := k.nested[name]; ok {
			out = append(out, child.table.leaves(child.path+".")...)
			continue
		}
		out = append(out, prefix+name)
	}
	return out
}

// missingGuideKeys は読み込みが受ける key のうち、案内の文面 text でその key を説明する節に出てこないものを、
// 「<節>: <key>」の綴りで返す。案内は key を、その key を説明する節の表の key の列に次の綴りで書く。設定の項目は「項目」の節に
// top level からの path (`tracker.kind`・`triggers[].when`)、述語は「issue 側の述語」「CL 側の述語」の節に when
// からの path (`labels.all`・`assignee`)。節を限るのは、例や違いの表に出る綴りで、説明の行が消えたのを見逃さないため。
// どの節も、key の表の後に provider の対応表 (述語の節は provider ごとの述語の意味、「項目」の節は置き場のサービスごとの違い) を
// 持ち、同じ key がどちらにも出る。対応表の目印から節の終わりまでを探す範囲から外し、key の表の行が消えたのを見逃さない。
// 値が対応表の key をここで辿り忘れると、その key の綴り (`tracker` など) を案内に探して落ちる。
// 節か、節の対応表の目印が無いときは error (目印の文面が変わって、黙って節の全体を探すのに戻らないように)
func missingGuideKeys(text string) ([]string, error) {
	c := &checker{}
	def := &Definition{}
	var issue trigger.IssuePredicate
	var cl trigger.CLPredicate
	var node *yaml.Node
	labels := keyTable{fields: c.labelsFields(new([]string), new([]string), new([]string))}
	names := keyTable{fields: c.namesFields(new([]string), new([]string))}
	predicateNesting := map[string]nestedTable{
		"labels": {"labels", labels}, "status": {"status", names}, "type": {"type", names},
	}
	roots := []struct {
		section string
		// providerTable は provider の対応表の始まりの目印
		providerTable string
		table         keyTable
	}{
		{"項目", serviceDifferenceTableMarker, keyTable{fields: c.topFields(def), nested: map[string]nestedTable{
			"tracker":   {"tracker", keyTable{fields: c.trackerFields(&def.Tracker, &placeInput{})}},
			"cl":        {"cl", keyTable{fields: c.clFields(&placeInput{})}},
			"polling":   {"polling", keyTable{fields: c.pollingFields(&def.Interval)}},
			"workspace": {"workspace", keyTable{fields: c.workspaceFields(def)}},
			"hooks":     {"hooks", keyTable{fields: c.hooksFields(&def.Hooks)}},
			"limits":    {"limits", keyTable{fields: c.limitsFields(def)}},
			"claude":    {"claude", keyTable{fields: c.claudeFields(def)}},
			"triggers":  {"triggers[]", keyTable{fields: c.triggerFields(&trigger.Trigger{}, map[string]bool{}, &deferredItems{})}},
		}}},
		// when の中は on で表が変わるので、述語の表は when からの path で別に辿る
		{"issue 側の述語", jiraProviderTableMarker, keyTable{fields: c.issuePredicateFields(&issue, &node, &node), nested: predicateNesting}},
		{"CL 側の述語", gitlabProviderTableMarker, keyTable{fields: c.clPredicateFields(&cl, &node), nested: predicateNesting}},
	}

	var missing []string
	for _, root := range roots {
		body, ok := section(text, root.section)
		if !ok {
			return nil, fmt.Errorf("案内に節 ### %s が無い", root.section)
		}
		end := strings.Index(body, root.providerTable)
		if end < 0 {
			return nil, fmt.Errorf("案内の節 ### %s に provider の対応表の目印 %q が無い", root.section, root.providerTable)
		}
		body = body[:end]
		cells := keyCells(body)
		for _, key := range root.table.leaves("") {
			if !slices.ContainsFunc(cells, func(cell string) bool { return strings.Contains(cell, "`"+key+"`") }) {
				missing = append(missing, root.section+": "+key)
			}
		}
	}
	slices.Sort(missing)
	return slices.Compact(missing), nil
}

// withoutRow は text の節 ### heading のうち provider の対応表の目印 marker より前から、key の列が `key` の表の行を
// 1 本消した文面を返す。範囲に行が無ければテストを落とす (差し替えが空振りするか別の表の行を消して、key の表の行が残るのを防ぐ)。
func withoutRow(t *testing.T, text, heading, marker, key string) string {
	t.Helper()
	start := strings.Index(text, "\n### "+heading+"\n")
	if start < 0 {
		t.Fatalf("案内 (internal/guide/guide.md) に節 ### %s が無い", heading)
	}
	end := strings.Index(text[start:], marker)
	if end < 0 {
		t.Fatalf("案内 (internal/guide/guide.md) の節 ### %s に目印 %q が無い", heading, marker)
	}
	row := strings.Index(text[start:start+end], "\n| `"+key+"` |")
	if row < 0 {
		t.Fatalf("案内 (internal/guide/guide.md) の節 ### %s の目印より前に key の列が `%s` の行が無い", heading, key)
	}
	from := start + row + 1
	to := from + strings.Index(text[from:], "\n") + 1
	return text[:from] + text[to:]
}

// without は text から s を 1 箇所消した文面を返す。s が無ければテストを落とす。
func without(t *testing.T, text, s string) string {
	t.Helper()
	if !strings.Contains(text, s) {
		t.Fatalf("案内 (internal/guide/guide.md) に %q が無い", s)
	}
	return strings.Replace(text, s, "", 1)
}

func TestEveryKeyTheLoaderAcceptsAppearsInTheGuide(t *testing.T) {
	missing, err := missingGuideKeys(guide.Text())

	if err != nil {
		t.Fatalf("案内 (internal/guide/guide.md) を読めない: %v", err)
	}
	if len(missing) > 0 {
		t.Fatalf("読み込みが受ける key が案内 (internal/guide/guide.md) に出てこない: %s", strings.Join(missing, ", "))
	}
}

func TestAKeyWhoseRowIsGoneIsMissingEvenIfTheProviderTableNamesIt(t *testing.T) {
	cases := []struct {
		heading string
		marker  string
		key     string
		want    string
	}{
		// `tracker.host` は置き場のサービスごとの違いの表にも、`draft` は gitlab の対応表にも、`assignee` は jira の対応表にも出る
		{"項目", serviceDifferenceTableMarker, "tracker.host", "項目: tracker.host"},
		{"CL 側の述語", gitlabProviderTableMarker, "draft", "CL 側の述語: draft"},
		{"issue 側の述語", jiraProviderTableMarker, "assignee", "issue 側の述語: assignee"},
	}
	for _, tc := range cases {
		t.Run(tc.heading, func(t *testing.T) {
			text := withoutRow(t, guide.Text(), tc.heading, tc.marker, tc.key)

			missing, err := missingGuideKeys(text)

			if err != nil {
				t.Fatalf("案内を読めない: %v", err)
			}
			if !slices.Contains(missing, tc.want) {
				t.Fatalf("行を消した key が欠けたと報告されない: want %q, got %v", tc.want, missing)
			}
		})
	}
}

func TestASectionWithoutItsProviderTableMarkerIsAnError(t *testing.T) {
	cases := []struct {
		name   string
		marker string
	}{
		{"項目", serviceDifferenceTableMarker},
		{"CL 側", gitlabProviderTableMarker},
		{"issue 側", jiraProviderTableMarker},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := without(t, guide.Text(), tc.marker)

			_, err := missingGuideKeys(text)

			if err == nil || !strings.Contains(err.Error(), tc.marker) {
				t.Fatalf("provider の対応表の目印が無いことを名指しして落ちない: %v", err)
			}
		})
	}
}
