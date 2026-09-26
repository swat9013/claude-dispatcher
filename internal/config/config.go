// Package config は宣言 config (config.toml) を読み、名指しで loud に検査する (docs/design/formats.md §2)。
//
// 検査は 2 段に分かれる。Load は file の中身だけで決まる検査 (未知の key・型・必須・綴り) で、外部を読まない。
// 置き場 repo と label の実在検査は gh を要するので tick 側が持つ。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// 機構が付ける label。着手可 label と triage label だけが config で綴りを変えられる
const (
	WIPLabel   = "dispatcher:wip"
	HumanLabel = "ready-for-human"
)

// SupportedTrackers は観測を実装済みの tracker。他は名指しで落とす (silent に空を観測しない)
var SupportedTrackers = []string{"gh"}

// schema は許す table と key。未知の綴りは名指しで落とす (「宣言していない」と同じ挙動にしない)
var schema = map[string][]string{
	"issue":  {"repo", "ready_label", "triage_label", "tracker"},
	"cl":     {"repo"},
	"limits": {"max_wip"},
	"auth":   {"token_file", "claude_token_file"},
}

// Error は config 起因の失敗。観測を開始しない。
type Error struct{ msg string }

func (e *Error) Error() string { return e.msg }

func errorf(format string, args ...any) error { return &Error{fmt.Sprintf(format, args...)} }

// IsError は err が config 起因の失敗かを返す。
func IsError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// Config は検査済みの宣言 config。
type Config struct {
	Path        string
	Tracker     string
	IssueRepo   string
	ReadyLabel  string
	TriageLabel string // 空なら残タスクの起票に label を付けない
	CLRepo      string // [cl] を省くと IssueRepo
	MaxWIP      int
	// TokenFile / ClaudeTokenFile は `~` を展開した絶対 path。空なら書かれていない
	TokenFile       string
	ClaudeTokenFile string
}

// Load は path の config を読んで検査する。home は token file の `~` の展開先。
func Load(path, home string) (Config, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Config{}, errorf("宣言 config が無い: %s", path)
	}
	if err != nil {
		return Config{}, errorf("宣言 config を読めない: %s (%v)", path, err)
	}
	var doc map[string]any
	if _, err := toml.Decode(string(raw), &doc); err != nil {
		return Config{}, errorf("%s: TOML として読めない: %v", path, err)
	}
	if err := checkSchema(path, doc); err != nil {
		return Config{}, err
	}

	issue, _ := doc["issue"].(map[string]any)
	c := Config{Path: path, Tracker: "gh"}
	var errs []string
	requireString := func(table map[string]any, name, key string, dst *string) {
		v, ok := table[key]
		if !ok {
			errs = append(errs, fmt.Sprintf("[%s].%s は必須", name, key))
			return
		}
		s, ok := v.(string)
		if !ok || s == "" {
			errs = append(errs, fmt.Sprintf("[%s].%s は非空の文字列", name, key))
			return
		}
		*dst = s
	}
	optionalString := func(table map[string]any, name, key string, dst *string) {
		v, ok := table[key]
		if !ok {
			return
		}
		s, ok := v.(string)
		if !ok || s == "" {
			errs = append(errs, fmt.Sprintf("[%s].%s は非空の文字列", name, key))
			return
		}
		*dst = s
	}

	if issue == nil {
		errs = append(errs, "[issue] は必須 (repo と ready_label を書く)")
		issue = map[string]any{}
	}
	requireString(issue, "issue", "repo", &c.IssueRepo)
	requireString(issue, "issue", "ready_label", &c.ReadyLabel)
	optionalString(issue, "issue", "triage_label", &c.TriageLabel)
	optionalString(issue, "issue", "tracker", &c.Tracker)

	c.CLRepo = c.IssueRepo
	if cl, ok := doc["cl"].(map[string]any); ok {
		if _, has := cl["repo"]; !has {
			errs = append(errs, "[cl] を書くなら [cl].repo は必須 (空の [cl] は「同じ」ではなく誤り)")
		} else {
			requireString(cl, "cl", "repo", &c.CLRepo)
		}
	}

	limits, _ := doc["limits"].(map[string]any)
	switch v := limits["max_wip"].(type) {
	case int64:
		if v < 1 {
			errs = append(errs, "[limits].max_wip は 1 以上の整数")
		}
		c.MaxWIP = int(v)
	case nil:
		errs = append(errs, "[limits].max_wip は必須 (1 以上の整数)")
	default:
		errs = append(errs, fmt.Sprintf("[limits].max_wip は 1 以上の整数 (%T が書かれている)", v))
	}

	if auth, ok := doc["auth"].(map[string]any); ok {
		if len(auth) == 0 {
			errs = append(errs, "[auth] を書くなら token_file か claude_token_file の少なくとも 1 つは必須")
		}
		for key, dst := range map[string]*string{"token_file": &c.TokenFile, "claude_token_file": &c.ClaudeTokenFile} {
			if _, has := auth[key]; !has {
				continue
			}
			var raw string
			optionalString(auth, "auth", key, &raw)
			if raw == "" {
				continue
			}
			file, ok := expandHome(raw, home)
			if !ok {
				// 相対 path は cwd (cron の cd 先) で指す先が変わる
				errs = append(errs, fmt.Sprintf("[auth].%s は絶対 path か ~ 始まり: %s", key, raw))
				continue
			}
			*dst = file
		}
	}

	if len(errs) > 0 {
		sort.Strings(errs)
		return Config{}, errorf("%s: %s", path, strings.Join(errs, " / "))
	}
	if !slices.Contains(SupportedTrackers, c.Tracker) {
		return Config{}, errorf("%s: [issue].tracker %q は未対応 (対応: %s)", path, c.Tracker, strings.Join(SupportedTrackers, ", "))
	}
	if c.TriageLabel == c.ReadyLabel {
		// 同じだと worker の起票が次 tick の候補になり、dispatcher が自分の作業を自己増殖させる
		return Config{}, errorf("%s: [issue].triage_label が ready_label と同じ綴り %q", path, c.ReadyLabel)
	}
	for _, repo := range []string{c.IssueRepo, c.CLRepo} {
		parts := strings.Split(repo, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return Config{}, errorf("%s: repo %q は owner/name の形でなければならない", path, repo)
		}
	}
	return c, nil
}

func checkSchema(path string, doc map[string]any) error {
	for table, value := range doc {
		allowed, known := schema[table]
		if !known {
			return errorf("%s: 未知の table [%s] (許すのは %s)", path, table, tableNames())
		}
		keys, ok := value.(map[string]any)
		if !ok {
			return errorf("%s: [%s] は table でなければならない", path, table)
		}
		var unknown []string
		for key := range keys {
			if !slices.Contains(allowed, key) {
				unknown = append(unknown, key)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return errorf("%s: [%s] に未知の key %s (許すのは %s)", path, table, strings.Join(unknown, ", "), strings.Join(allowed, ", "))
		}
	}
	return nil
}

func tableNames() string {
	names := make([]string, 0, len(schema))
	for name := range schema {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func expandHome(raw, home string) (string, bool) {
	switch {
	case raw == "~":
		return home, true
	case strings.HasPrefix(raw, "~/"):
		return filepath.Join(home, raw[2:]), true
	case filepath.IsAbs(raw):
		return raw, true
	}
	return "", false
}
