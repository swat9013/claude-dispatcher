// Package state は scope key ごとの state dir と、loop の生存期間の lock を持つ (formats.md §1 / §6)。
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

const appName = "claude-dispatcher"

// Root は state root。XDG_STATE_HOME を、無ければ HOME の既定を使う。macOS でも XDG に揃える。
func Root(getenv func(string) string) string {
	base := getenv("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(getenv("HOME"), ".local", "state")
	}
	return filepath.Join(base, appName)
}

var unsafeRune = regexp.MustCompile(`[^a-z0-9._-]`)

// Dir は scope key の state dir。無害化した scope key に、scope key の sha256 の先頭 8 文字を足した名前にする。
// hash を足すのは、無害化で別の scope key が同じ綴りになっても置き場を分けるため。
func Dir(root, scopeKey string) string {
	sum := sha256.Sum256([]byte(scopeKey))
	return filepath.Join(root, unsafeRune.ReplaceAllString(strings.ToLower(scopeKey), "_")+"-"+hex.EncodeToString(sum[:])[:8])
}

// ErrAlreadyRunning は同じ scope key の loop が走っていて lock を取れなかったこと。
var ErrAlreadyRunning = errors.New("同じ scope key の loop がもう走っている")

// Lock は state dir を作り、loop の生存期間の lock を取る。返した file を閉じると lock が外れる — loop が終わるまで開いておく。
// flock は process が消えれば外れるので、落ちた loop の lock が次の起動を塞ぐことはない。
func Lock(dir, scopeKey string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("state dir を作れない (%s): %w", dir, err)
	}
	file := filepath.Join(dir, "loop.lock")
	lock, err := os.OpenFile(file, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("loop の lock file を開けない (%s): %w", file, err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%s: %w (%s)", scopeKey, ErrAlreadyRunning, file)
		}
		return nil, fmt.Errorf("loop の lock を取れない (%s): %w", file, err)
	}
	return lock, nil
}
