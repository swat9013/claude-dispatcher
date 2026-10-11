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

func TestEveryKeyTheLoaderAcceptsAppearsInTheGuide(t *testing.T) {
	// 案内は key を、その key を説明する節の中に次の綴りで書く。設定の項目は「項目」の節に top level からの path
	// (`tracker.kind`・`triggers[].when`)、述語は「issue 側の述語」「CL 側の述語」の節に when からの path (`labels.all`・`assignee`)。
	// 節を限るのは、例や違いの表に出る綴りで、説明の行が消えたのを見逃さないため。値が対応表の key をここで辿り忘れると、その
	// key の綴り (`tracker` など) を案内に探して落ちる
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
		table   keyTable
	}{
		{"項目", keyTable{fields: c.topFields(def), nested: map[string]nestedTable{
			"tracker":   {"tracker", keyTable{fields: c.trackerFields(&def.Tracker, new(bool))}},
			"polling":   {"polling", keyTable{fields: c.pollingFields(&def.Interval)}},
			"workspace": {"workspace", keyTable{fields: c.workspaceFields(def)}},
			"hooks":     {"hooks", keyTable{fields: c.hooksFields(&def.Hooks)}},
			"limits":    {"limits", keyTable{fields: c.limitsFields(def)}},
			"claude":    {"claude", keyTable{fields: c.claudeFields(def)}},
			"triggers":  {"triggers[]", keyTable{fields: c.triggerFields(&trigger.Trigger{}, map[string]bool{}, &deferredItems{})}},
		}}},
		// when の中は on で表が変わるので、述語の表は when からの path で別に辿る
		{"issue 側の述語", keyTable{fields: c.issuePredicateFields(&issue, &node, &node), nested: predicateNesting}},
		{"CL 側の述語", keyTable{fields: c.clPredicateFields(&cl, &node), nested: predicateNesting}},
	}
	text := guide.Text()

	var missing []string
	for _, root := range roots {
		body, ok := section(text, root.section)
		if !ok {
			t.Fatalf("案内 (internal/guide/guide.md) に節 ### %s が無い", root.section)
		}
		for _, key := range root.table.leaves("") {
			if !strings.Contains(body, "`"+key+"`") {
				missing = append(missing, root.section+": "+key)
			}
		}
	}

	slices.Sort(missing)
	if missing = slices.Compact(missing); len(missing) > 0 {
		t.Fatalf("読み込みが受ける key が案内 (internal/guide/guide.md) に出てこない: %s", strings.Join(missing, ", "))
	}
}
