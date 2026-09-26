// Package deps は依存 CLI (gh / claude) の path を解決する。cron の最小環境では PATH が通らない (system.md §9)。
package deps

import (
	"os"
	"path/filepath"
	"strings"
)

// Names は tick が起動する依存 CLI
var Names = []string{"gh", "claude"}

// candidates は PATH に無いときに足す置き場。前に居るものから探す
var candidates = []string{"~/.local/bin", "~/.local/share/mise/shims", "/opt/homebrew/bin", "/usr/local/bin"}

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
