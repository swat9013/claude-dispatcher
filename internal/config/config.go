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
	IssueRepo   Repo
	ReadyLabel  string
	TriageLabel string // 空なら残タスクの起票に label を付けない
	CLRepo      Repo   // [cl] を省くと IssueRepo
	MaxWIP      int
	// TokenFile / ClaudeTokenFile は `~` を展開した絶対 path。空なら書かれていない
	TokenFile       string
	ClaudeTokenFile string
}

// document は config.toml の綴り (formats.md §2)。値は書かれたかどうかを区別するため pointer で受ける。
// ここに無い table / key は未知の綴りとして名指しで落とす (「宣言していない」と同じ挙動にしない)。
type document struct {
	Issue struct {
		Repo        *string `toml:"repo"`
		ReadyLabel  *string `toml:"ready_label"`
		TriageLabel *string `toml:"triage_label"`
		Tracker     *string `toml:"tracker"`
	} `toml:"issue"`
	CL struct {
		Repo *string `toml:"repo"`
	} `toml:"cl"`
	Limits struct {
		MaxWIP *int64 `toml:"max_wip"`
	} `toml:"limits"`
	Auth struct {
		TokenFile       *string `toml:"token_file"`
		ClaudeTokenFile *string `toml:"claude_token_file"`
	} `toml:"auth"`
}

// RequireFile は宣言 config の file が在ることを確かめる。無ければ config 起因の失敗。
func RequireFile(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return errorf("宣言 config が無い: %s", path)
	}
	return nil
}

// Load は path の config を読んで検査する。home は token file の `~` の展開先。
func Load(path, home string) (Config, error) {
	if err := RequireFile(path); err != nil {
		return Config{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, errorf("宣言 config を読めない: %s (%v)", path, err)
	}
	var doc document
	md, err := toml.Decode(string(raw), &doc)
	if err != nil {
		return Config{}, errorf("%s: TOML として読めないか、値の型が違う: %v", path, err)
	}
	if unknown := md.Undecoded(); len(unknown) > 0 {
		names := make([]string, 0, len(unknown))
		for _, key := range unknown {
			names = append(names, key.String())
		}
		return Config{}, errorf("%s: 未知の綴り %s (許す table と key は docs/design/formats.md §2)", path, strings.Join(names, ", "))
	}

	c := Config{Path: path, Tracker: "gh"}
	var errs []string
	str := func(name string, v *string, required bool, dst *string) {
		switch {
		case v == nil && required:
			errs = append(errs, name+" は必須")
		case v != nil && *v == "":
			errs = append(errs, name+" は非空の文字列")
		case v != nil:
			*dst = *v
		}
	}
	var issueRepo, clRepo, tokenFile, claudeTokenFile string
	str("[issue].repo", doc.Issue.Repo, true, &issueRepo)
	str("[issue].ready_label", doc.Issue.ReadyLabel, true, &c.ReadyLabel)
	str("[issue].triage_label", doc.Issue.TriageLabel, false, &c.TriageLabel)
	str("[issue].tracker", doc.Issue.Tracker, false, &c.Tracker)
	// 空の [cl] / [auth] は「省いた」ではなく書きかけの誤り
	str("[cl].repo", doc.CL.Repo, md.IsDefined("cl"), &clRepo)
	if md.IsDefined("auth") && doc.Auth.TokenFile == nil && doc.Auth.ClaudeTokenFile == nil {
		errs = append(errs, "[auth] を書くなら token_file か claude_token_file の少なくとも 1 つは必須")
	}
	str("[auth].token_file", doc.Auth.TokenFile, false, &tokenFile)
	str("[auth].claude_token_file", doc.Auth.ClaudeTokenFile, false, &claudeTokenFile)
	switch {
	case doc.Limits.MaxWIP == nil:
		errs = append(errs, "[limits].max_wip は必須 (1 以上の整数)")
	case *doc.Limits.MaxWIP < 1:
		errs = append(errs, "[limits].max_wip は 1 以上の整数")
	default:
		c.MaxWIP = int(*doc.Limits.MaxWIP)
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
	if c.IssueRepo, err = ParseRepo(issueRepo); err != nil {
		return Config{}, errorf("%s: [issue].%v", path, err)
	}
	c.CLRepo = c.IssueRepo
	if clRepo != "" {
		if c.CLRepo, err = ParseRepo(clRepo); err != nil {
			return Config{}, errorf("%s: [cl].%v", path, err)
		}
	}
	for key, file := range map[string]struct {
		raw string
		dst *string
	}{"token_file": {tokenFile, &c.TokenFile}, "claude_token_file": {claudeTokenFile, &c.ClaudeTokenFile}} {
		if file.raw == "" {
			continue
		}
		abs, ok := expandHome(file.raw, home)
		if !ok {
			// 相対 path は cwd (loop を撃った clone) で指す先が変わる
			return Config{}, errorf("%s: [auth].%s は絶対 path か ~ 始まり: %s", path, key, file.raw)
		}
		*file.dst = abs
	}
	return c, nil
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
