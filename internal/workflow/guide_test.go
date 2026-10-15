package workflow

import (
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

// paths は leaves に、値が対応表の key 自身の path (`cl`・`triggers`) を加えたもの。案内の key の表に書いてよい綴り。
func (k keyTable) paths(prefix string) []string {
	var out []string
	for name := range k.fields {
		if child, ok := k.nested[name]; ok {
			out = append(out, prefix+strings.TrimSuffix(child.path, "[]"))
			out = append(out, child.table.paths(child.path+".")...)
			continue
		}
		out = append(out, prefix+name)
	}
	return out
}

// loaderKeys は読み込みが受ける key を、それを説明する案内の節ごとに、案内での path の綴りで返す。設定の項目は「項目」の節に
// top level からの path (`tracker.kind`・`triggers[].when`)、述語は「issue 側の述語」「CL 側の述語」の節に when からの path
// (`labels.all`・`assignee`)。値が対応表の key をここで辿り忘れると、その key の綴り (`tracker` など) を案内に探して落ちる。
func loaderKeys() map[string]keyTable {
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
	top := keyTable{fields: c.topFields(def), nested: map[string]nestedTable{
		"tracker":   {"tracker", keyTable{fields: c.trackerFields(&def.Tracker, &placeInput{})}},
		"cl":        {"cl", keyTable{fields: c.clFields(&placeInput{})}},
		"polling":   {"polling", keyTable{fields: c.pollingFields(&def.Interval)}},
		"workspace": {"workspace", keyTable{fields: c.workspaceFields(def)}},
		"hooks":     {"hooks", keyTable{fields: c.hooksFields(&def.Hooks)}},
		"limits":    {"limits", keyTable{fields: c.limitsFields(def)}},
		"claude":    {"claude", keyTable{fields: c.claudeFields(def)}},
		"triggers":  {"triggers[]", keyTable{fields: c.triggerFields(&trigger.Trigger{}, map[string]bool{}, &deferredItems{})}},
	}}
	// when の中は on で表が変わるので、述語の表は when からの path で別に辿る
	return map[string]keyTable{
		"項目":         top,
		"issue 側の述語": {fields: c.issuePredicateFields(&issue, &node, &node), nested: predicateNesting},
		"CL 側の述語":    {fields: c.clPredicateFields(&cl, &node), nested: predicateNesting},
	}
}

// guideKeyCells は案内 text の節 title の key の表の、行ごとの 1 列目のセル。key の表だけを読むのは、例や provider の対応表に
// 出る綴りで、key の表の行が消えたのを見逃さないため。
func guideKeyCells(text, title string) ([]string, error) {
	s, err := guide.FindSection(text, title)
	if err != nil {
		return nil, err
	}
	tables, err := s.KeyTables()
	if err != nil {
		return nil, err
	}
	var cells []string
	for _, t := range tables {
		for _, row := range t.Rows {
			cells = append(cells, row[0])
		}
	}
	return cells, nil
}

// covers は key の表の 1 列目のセルのどれかが key を `key` の綴りで含むこと (`labels.all` / `labels.any` の行がある)。
func covers(cells []string, key string) bool {
	return slices.ContainsFunc(cells, func(cell string) bool { return strings.Contains(cell, "`"+key+"`") })
}

// missingGuideKeys は読み込みが受ける key のうち、案内 text でその key を説明する節の key の表に出てこないものを、
// 「<節>: <key>」の綴りで返す。
func missingGuideKeys(text string) ([]string, error) {
	var missing []string
	for title, table := range loaderKeys() {
		cells, err := guideKeyCells(text, title)
		if err != nil {
			return nil, err
		}
		for _, key := range table.leaves("") {
			if !covers(cells, key) {
				missing = append(missing, title+": "+key)
			}
		}
	}
	slices.Sort(missing)
	return slices.Compact(missing), nil
}

// unknownGuideKeys は案内 text の key の表の 1 列目に “ で書いた綴りのうち、読み込みが受けないものを「<節>: <綴り>」で返す。
func unknownGuideKeys(text string) ([]string, error) {
	var unknown []string
	for title, table := range loaderKeys() {
		cells, err := guideKeyCells(text, title)
		if err != nil {
			return nil, err
		}
		keys := table.paths("")
		for _, cell := range cells {
			for _, name := range backquoted(cell) {
				if !slices.Contains(keys, name) {
					unknown = append(unknown, title+": "+name)
				}
			}
		}
	}
	slices.Sort(unknown)
	return unknown, nil
}

// backquoted はセルの “ で囲んだ綴りの列。
func backquoted(cell string) []string {
	parts := strings.Split(cell, "`")
	var out []string
	for i := 1; i < len(parts); i += 2 {
		out = append(out, parts[i])
	}
	return out
}

// withoutKeyRow は text の節 title の key の表から、1 列目が `key` の行を消した文面を返す。行が無ければテストを落とす。
func withoutKeyRow(t *testing.T, text, title, key string) string {
	t.Helper()
	cells, err := guideKeyCells(text, title)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cells, "`"+key+"`") {
		t.Fatalf("案内の節 %s の key の表に `%s` の行が無い", title, key)
	}
	start := strings.Index(text, "### "+title+"\n")
	row := strings.Index(text[start:], "\n| `"+key+"` |")
	from := start + row + 1
	to := from + strings.Index(text[from:], "\n") + 1
	return text[:from] + text[to:]
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

func TestEveryKeyInTheGuideIsAcceptedByTheLoader(t *testing.T) {
	unknown, err := unknownGuideKeys(guide.Text())

	if err != nil {
		t.Fatalf("案内 (internal/guide/guide.md) を読めない: %v", err)
	}
	if len(unknown) > 0 {
		t.Fatalf("案内 (internal/guide/guide.md) の key の表に読み込みが受けない key がある: %s", strings.Join(unknown, ", "))
	}
}

func TestAKeyNotAcceptedByTheLoaderIsReported(t *testing.T) {
	text := strings.Replace(guide.Text(), "\n| `cl.kind` |", "\n| `cl.nope` | 文字列 | 無い key |\n| `cl.kind` |", 1)

	unknown, err := unknownGuideKeys(text)

	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(unknown, "項目: cl.nope") {
		t.Fatalf("読み込みが受けない key が報告されない: %v", unknown)
	}
}

func TestAKeyWhoseRowIsGoneIsMissingEvenIfTheProviderTableNamesIt(t *testing.T) {
	cases := []struct {
		title string
		key   string
	}{
		// `tracker.host` は置き場のサービスごとの違いの表にも、`draft` は gitlab の対応表にも、`assignee` は jira の対応表にも出る
		{"項目", "tracker.host"},
		{"CL 側の述語", "draft"},
		{"issue 側の述語", "assignee"},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			text := withoutKeyRow(t, guide.Text(), tc.title, tc.key)

			missing, err := missingGuideKeys(text)

			if err != nil {
				t.Fatalf("案内を読めない: %v", err)
			}
			if want := tc.title + ": " + tc.key; !slices.Contains(missing, want) {
				t.Fatalf("行を消した key が欠けたと報告されない: want %q, got %v", want, missing)
			}
		})
	}
}

func TestRewordingTheGuideProseDoesNotBreakTheSync(t *testing.T) {
	// 以前は散文の一文を目印にしていた。散文を書き換えても key の表の読み方は変わらない
	text := strings.Replace(guide.Text(), "上の表は `github` の field で書いた。", "別の言い回し。", 1)
	text = strings.Replace(text, "**置き場のサービスごとの違い**", "違い", 1)

	missing, err := missingGuideKeys(text)

	if err != nil || len(missing) > 0 {
		t.Fatalf("散文の書き換えで落ちた: %v %v", err, missing)
	}
}
