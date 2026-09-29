// Package deps は依存 CLI (gh / claude / git) の path を解決する。loop は起動した shell の PATH を継ぐが、最小の PATH の
// shell (ssh 越し等) から撃たれても動くように、よく使われる置き場も探す (system.md §9)。
package deps

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Names は PATH を自己解決する依存 CLI。git は tick 自身は撃たないが、同じ PATH を継ぐ orchestrator / worker が撃つ
var Names = []string{"gh", "claude", "git"}

// Invoked は tick 自身が起動する依存 CLI。試運転は起動の手前で止まるので、これらが解決できるかを代わりに検査する
var Invoked = []string{"gh", "claude"}

// candidates は PATH に無いときに足す置き場。前に居るものから探す (Homebrew は macOS の 2 つと Linux の prefix)。
// HOME の外の置き場を変えたら、black-box テストの selfResolutionDirs も揃える
var candidates = []string{
	"~/.local/bin", "~/.local/share/mise/shims",
	"/opt/homebrew/bin", "/usr/local/bin", "/home/linuxbrew/.linuxbrew/bin",
}

// ResolvePATH は依存 CLI がすべて PATH で見つかれば PATH をそのまま、見つからないものがあれば
// 実在する候補の置き場を前置した PATH を返す。
func ResolvePATH(path, home string) string {
	missing := false
	for _, name := range Names {
		if LookPath(name, path) == "" {
			missing = true
		}
	}
	if !missing {
		return path
	}
	var dirs []string
	for _, c := range candidates {
		dir := c
		if strings.HasPrefix(c, "~/") {
			dir = filepath.Join(home, c[2:])
		}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			dirs = append(dirs, dir)
		}
	}
	if path != "" {
		dirs = append(dirs, path)
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

// LookPath は path (PATH の書式) から実行できる name を探して絶対 path を返す。無ければ ""。
// exec.LookPath は自プロセスの PATH を見るので、子プロセスへ渡す PATH で探すためにここで持つ。
func LookPath(name, path string) string {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		file := filepath.Join(dir, name)
		if info, err := os.Stat(file); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			abs, err := filepath.Abs(file)
			if err != nil {
				return file
			}
			return abs
		}
	}
	return ""
}

// Lookup は env の PATH から name を探して絶対 path を返す。見つからなければ PATH を添えた error。
func Lookup(name string, env []string) (string, error) {
	path := Getenv(env, "PATH")
	if file := LookPath(name, path); file != "" {
		return file, nil
	}
	return "", fmt.Errorf("%s が PATH に無い (PATH=%s)", name, path)
}

// Getenv は env ("KEY=value" の列) から key の値を返す。同じ key が複数あれば後ろが勝つ (exec と同じ)。
func Getenv(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return env[i][len(prefix):]
		}
	}
	return ""
}

// WithEnv は env の set の key を置き換えた (無ければ足した) 新しい列を返す。
func WithEnv(env []string, set map[string]string) []string {
	out := make([]string, 0, len(env)+len(set))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if _, replaced := set[key]; !replaced {
			out = append(out, kv)
		}
	}
	for key, value := range set {
		out = append(out, key+"="+value)
	}
	return out
}
