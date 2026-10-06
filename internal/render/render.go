// Package render は action と共通 prompt の template を描画する (system.md §10、formats.md §2.7)。
// 未知の変数と未知の関数は描画の失敗にする。
package render

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/swat9013/claude-dispatcher/internal/target"
)

// Vars は template に渡す変数。
type Vars struct {
	// Item は worker の作業対象。issue なら `.issue`、CL なら `.cl` の変数になる
	Item      target.Item
	Trigger   string
	Attempt   int
	Workspace string
}

// data は Vars を template の変数の名前 (`.issue.number` など) に写す。map にするのは、未知の変数を
// missingkey=error で描画の失敗にするため (struct の field は template の綴りと合わない)。
func (v Vars) data() map[string]any {
	data := map[string]any{
		"trigger":   map[string]any{"name": v.Trigger},
		"attempt":   v.Attempt,
		"workspace": v.Workspace,
	}
	switch item := v.Item.(type) {
	case target.Issue:
		issue := map[string]any{
			"number": item.Number,
			"title":  item.Title,
			"url":    item.URL,
			"labels": nonNil(item.Labels),
		}
		// key は key で指す tracker (Jira) の issue にだけ置く。他の tracker で `.issue.key` を書けば未知の変数になる
		if item.Key != "" {
			issue["key"] = item.Key
		}
		data["issue"] = issue
	case target.CL:
		data["cl"] = map[string]any{
			"number": item.Number,
			"title":  item.Title,
			"url":    item.URL,
			"labels": nonNil(item.Labels),
			"head":   item.Head,
		}
	}
	return data
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Render は text を描画する。name は失敗を名指しするときの綴り (`action` など)。
func Render(name, text string, vars Vars) (string, error) {
	t, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("%s の描画の失敗: %w", name, err)
	}
	var out strings.Builder
	if err := t.Execute(&out, vars.data()); err != nil {
		return "", fmt.Errorf("%s の描画の失敗: %w", name, err)
	}
	return out.String(), nil
}

// Sample は kind の作業対象の見本。条件の分岐の中の変数まで確かめるよう、どの値も空にしない。key で指す tracker の issue の
// 見本は、呼び出し側が Key を足す。
func Sample(kind target.Kind) target.Item {
	if kind == target.KindCL {
		return target.CL{Number: 1, Title: "title", URL: "https://example.com/1", Labels: []string{"label"}, Head: "branch"}
	}
	return target.Issue{Number: 1, Title: "title", URL: "https://example.com/1", Labels: []string{"label"}}
}

// Check は text を見本の作業対象 sample の変数で描画してみて、描画の失敗 (綴りの誤り・未知の変数・未知の関数) を返す。
// workflow 定義の検査で、作業対象を読む前に落とすために使う。
func Check(name, text string, sample target.Item) error {
	_, err := Render(name, text, Vars{Item: sample, Trigger: "trigger", Attempt: 1, Workspace: "/workspace"})
	return err
}
