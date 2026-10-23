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
	"strconv"
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

// Sanitize は s を小文字にし、[a-z0-9._-] 以外の文字を _ に置き換える (formats.md §1 の無害化)。
func Sanitize(s string) string { return unsafeRune.ReplaceAllString(strings.ToLower(s), "_") }

// DirName は scope key の scope dir: 無害化した scope key に、scope key の sha256 の先頭 8 文字を足した名前。
// hash を足すのは、無害化で別の scope key が同じ綴りになっても置き場を分けるため。state dir と coordination board の ref (formats.md §12)
// が同じ名前を使う。
func DirName(scopeKey string) string {
	sum := sha256.Sum256([]byte(scopeKey))
	return Sanitize(scopeKey) + "-" + hex.EncodeToString(sum[:])[:8]
}

// Dir は scope key の state dir。
func Dir(root, scopeKey string) string { return filepath.Join(root, DirName(scopeKey)) }

// ErrAlreadyRunning は同じ scope key の loop か単発起動が走っていて lock を取れなかったこと。
var ErrAlreadyRunning = errors.New("同じ scope key の loop か単発起動がもう走っている")

// state dir の中の置き場の名前
const (
	// LogFile は log.jsonl (formats.md §4)
	LogFile = "log.jsonl"
	// lockFile は loop と単発起動の生存期間の lock。同じ scope key の 2 本目を止める
	lockFile = "loop.lock"
	// aliveFile は loop の生死を status に見せる lock。status はこちらだけを確かめるので、loop.lock を取り合わない
	aliveFile = "alive.lock"
)

// Held は loop か単発起動が生存期間のあいだ持つ lock。単発起動は alive を持たない (nil)。
type Held struct{ lock, alive *os.File }

// Close は lock を外す。
func (h *Held) Close() error {
	var err error
	if h.alive != nil {
		err = h.alive.Close()
	}
	return errors.Join(err, h.lock.Close())
}

// Lock は state dir を作り、loop の生存期間の lock (loop.lock と alive.lock) を取る。返した lock を閉じると外れる — loop が
// 終わるまで開いておく。flock は process が消えれば外れるので、落ちた loop の lock が次の起動を塞ぐことはない。
// loop.lock を取った後、alive.lock を取る前に、前の loop が max-concurrent で渡した並列上限を消す。
func Lock(dir, scopeKey string) (*Held, error) {
	lock, err := lockScope(dir, scopeKey)
	if err != nil {
		return nil, err
	}
	// 前の loop が残した並列上限は、alive.lock を取る前に消す。取った後に消すと、その間に撃った max-concurrent が loop を
	// 生きていると見て書き、成功を返した値を消してしまう (formats.md §6 の起動時の検査)
	if err := RemoveMaxConcurrent(dir); err != nil {
		lock.Close()
		return nil, err
	}
	// loop.lock を持つのは 1 本だけなので、alive.lock を取り合うのは生死を確かめる status の一瞬の共有 lock だけ。
	// それが外れるまで待つ
	file := filepath.Join(dir, aliveFile)
	alive, err := os.OpenFile(file, os.O_CREATE|os.O_RDWR, 0o644)
	if err == nil {
		err = syscall.Flock(int(alive.Fd()), syscall.LOCK_EX)
	}
	if err != nil {
		if alive != nil {
			alive.Close()
		}
		lock.Close()
		return nil, fmt.Errorf("loop の生死の lock を取れない (%s): %w", file, err)
	}
	return &Held{lock: lock, alive: alive}, nil
}

// LockRun は state dir を作り、単発起動の lock (loop.lock だけ。alive.lock は取らない) を取る (formats.md §11)。
func LockRun(dir, scopeKey string) (*Held, error) {
	lock, err := lockScope(dir, scopeKey)
	if err != nil {
		return nil, err
	}
	return &Held{lock: lock}, nil
}

// lockScope は state dir を作り、scope key の排他の lock (loop.lock) を待たずに取る。
func lockScope(dir, scopeKey string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("state dir を作れない (%s): %w", dir, err)
	}
	file := filepath.Join(dir, lockFile)
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

// Alive は、dir の loop が生きているか (alive.lock を誰かが持っているか) を返す。lock file を作らず、持っていなければ
// 直ちに外す。
func Alive(dir string) (bool, error) {
	held, err := Locked(filepath.Join(dir, aliveFile))
	if err != nil {
		return false, fmt.Errorf("loop の生死を確かめられない: %w", err)
	}
	return held, nil
}

// Locked は、file の flock を誰かが排他で持っているかを返す。共有の lock を待たずに試し、取れればすぐ外す。file が無ければ
// 誰も持っていない。lock file を作らない。process の生死を、その process が受け継いだ fd の flock で確かめるのに使う
// (pid は再利用されるので見ない。system.md §7)。
func Locked(file string) (bool, error) {
	lock, err := os.Open(file)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock file を開けない: %w", err)
	}
	defer lock.Close()
	err = syscall.Flock(int(lock.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	switch {
	case errors.Is(err, syscall.EWOULDBLOCK):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("flock を試せない (%s): %w", file, err)
	}
	return false, syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
}

// MaxConcurrentFile は max-concurrent が渡した並列上限を置く file (formats.md §1・§13)。max-concurrent が書き、loop が
// tick ごとに読み、起動時に消す
const MaxConcurrentFile = "max_concurrent"

// WriteMaxConcurrent は並列上限 n を書く。loop に書きかけを見せないよう、同じ dir の一時 file から rename する。一時 file は
// 呼び出しごとに名前を変え、同時に撃った呼び出しが取り合わないようにする。書けなければ一時 file を消す。
func WriteMaxConcurrent(dir string, n int) error {
	tmp, err := os.CreateTemp(dir, MaxConcurrentFile+".*.tmp")
	if err != nil {
		return fmt.Errorf("並列上限を書けない: %w", err)
	}
	// CreateTemp は 0600 で作るので、state dir の他の file と同じ 0644 に揃える
	_, err = tmp.WriteString(strconv.Itoa(n) + "\n")
	err = errors.Join(err, tmp.Chmod(0o644), tmp.Close())
	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(dir, MaxConcurrentFile))
	}
	if err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("並列上限を書けない: %w", err)
	}
	return nil
}

// ReadMaxConcurrent は max-concurrent が渡した並列上限を読む。無ければ ok が false。1 以上の整数でなければ誤り
// (黙って前の値を使い続けると、渡したつもりの値が効いていないことに気づけない。system.md §7)。
func ReadMaxConcurrent(dir string) (n int, ok bool, err error) {
	file := filepath.Join(dir, MaxConcurrentFile)
	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("max-concurrent で渡した並列上限を読めない: %w", err)
	}
	text := strings.TrimSpace(string(raw))
	n, err = strconv.Atoi(text)
	if err != nil || n < 1 {
		return 0, false, fmt.Errorf("max-concurrent で渡した並列上限が 1 以上の整数でない (%s: %q)", file, text)
	}
	return n, true, nil
}

// RemoveMaxConcurrent は max-concurrent が渡した並列上限を消す。前の loop が残した値を次の loop に継がない。無ければ何もしない。
func RemoveMaxConcurrent(dir string) error {
	if err := os.Remove(filepath.Join(dir, MaxConcurrentFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("前の loop が max-concurrent で渡した並列上限を消せない: %w", err)
	}
	return nil
}
