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

func TestEveryKeyTheLoaderAcceptsAppearsInTheGuide(t *testing.T) {
	// 案内は key を次の綴りで書く。設定の項目は top level からの path (`tracker.kind`・`triggers[].when`)、述語は when からの
	// path (`labels.all`・`assignee`)。値が対応表の key をここで辿り忘れると、その key の綴り (`tracker` など) を案内に探して落ちる
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
		prefix string
		table  keyTable
	}{
		{"", keyTable{fields: c.topFields(def), nested: map[string]nestedTable{
			"tracker":   {"tracker", keyTable{fields: c.trackerFields(&def.Tracker, new(bool))}},
			"polling":   {"polling", keyTable{fields: c.pollingFields(&def.Interval)}},
			"workspace": {"workspace", keyTable{fields: c.workspaceFields(def)}},
			"hooks":     {"hooks", keyTable{fields: c.hooksFields(&def.Hooks)}},
			"limits":    {"limits", keyTable{fields: c.limitsFields(def)}},
			"claude":    {"claude", keyTable{fields: c.claudeFields(def)}},
			"triggers":  {"triggers[]", keyTable{fields: c.triggerFields(&trigger.Trigger{}, map[string]bool{}, &deferredItems{})}},
		}}},
		// when の中は on で表が変わるので、述語の表は when からの path で別に辿る
		{"", keyTable{fields: c.issuePredicateFields(&issue, &node, &node), nested: predicateNesting}},
		{"", keyTable{fields: c.clPredicateFields(&cl, &node), nested: predicateNesting}},
	}
	text := guide.Text()

	var missing []string
	for _, root := range roots {
		for _, key := range root.table.leaves(root.prefix) {
			if !strings.Contains(text, "`"+key+"`") {
				missing = append(missing, key)
			}
		}
	}

	slices.Sort(missing)
	if missing = slices.Compact(missing); len(missing) > 0 {
		t.Fatalf("読み込みが受ける key が案内 (internal/guide/guide.md) に出てこない: %s", strings.Join(missing, ", "))
	}
}
