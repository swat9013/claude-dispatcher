package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/swat9013/claude-dispatcher/internal/target"
)

// scopeKeyFile は workspace root を使う scope key を書く file (formats.md §1・§6)。
const scopeKeyFile = ".scope-key"

// RootInUse は workspace root を別の scope key が使っていること。掃除は root の下をすべて自分の project のものとして読むので、
// 共有すると相手の workspace を終端と読んで消しうる (system.md §7)。
type RootInUse struct {
	Root string
	// ScopeKey は root を使っている scope key。file から読んだ値で、表示できる文字だけとは限らない
	ScopeKey string
}

func (e *RootInUse) Error() string {
	return fmt.Sprintf("workspace.root %s は scope key %s が使っている", e.Root, e.ScopeKey)
}

// ForeignWorkspace は .scope-key の無い root に、自分のものでない workspace があること。.scope-key を書く前の版の loop が
// 使っていた root では、別の project の workspace を掃除が消しうる (system.md §7)。
type ForeignWorkspace struct {
	Root string
	Ref  target.Ref
}

func (e *ForeignWorkspace) Error() string {
	return fmt.Sprintf("workspace.root %s に別の project の workspace %s がある", e.Root, e.Ref.FileName())
}

// CheckRoot は root を scope key が使えるかを読むだけで確かめる。別の scope key が書いてあれば *RootInUse。.scope-key が無く、
// launched が false を返す workspace があれば *ForeignWorkspace。launched は作業対象に自分が worker を起動したことがあるかを返す。
func CheckRoot(root, scopeKey string, launched func(target.Ref) (bool, error)) error {
	owner, err := rootOwner(root)
	if err != nil {
		return err
	}
	if owner != "" {
		if owner != scopeKey {
			return &RootInUse{Root: root, ScopeKey: owner}
		}
		return nil
	}
	refs, err := Manager{Root: root}.Existing()
	if err != nil {
		return err
	}
	for _, ref := range refs {
		own, err := launched(ref)
		if err != nil {
			return err
		}
		if !own {
			return &ForeignWorkspace{Root: root, Ref: ref}
		}
	}
	return nil
}

// ClaimRoot は root を scope key のものにする。誰のものでもなければ root を作って scope key を書く。別の scope key が書いて
// あれば *RootInUse。同時に撃った別の scope key に先を越されたときも、読み直して *RootInUse を返す。自分のものでない workspace が
// あれば書かずに *ForeignWorkspace (CheckRoot)。
func ClaimRoot(root, scopeKey string, launched func(target.Ref) (bool, error)) error {
	if err := CheckRoot(root, scopeKey, launched); err != nil {
		return err
	}
	owner, err := rootOwner(root)
	if err != nil {
		return err
	}
	if owner == "" {
		if err := writeRootOwner(root, scopeKey); err != nil {
			return err
		}
	}
	return CheckRoot(root, scopeKey, launched)
}

// rootOwner は root の scope key を読む。無ければ ""。
func rootOwner(root string) (string, error) {
	file := filepath.Join(root, scopeKeyFile)
	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		info, lerr := os.Lstat(file)
		if lerr != nil {
			return "", nil
		}
		// 指す先の無い symlink は、読むと無いが link すると既にあるので、書けないまま専有が成り立たない
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("workspace root の scope key を読めない: %s は指す先の無い symlink", file)
		}
		// 読んだ後に、同時に撃った書き手が置いた
		return rootOwner(root)
	}
	if err != nil {
		return "", fmt.Errorf("workspace root の scope key を読めない: %w", err)
	}
	owner := strings.TrimSpace(string(raw))
	if owner == "" {
		return "", fmt.Errorf("workspace root の scope key を読めない: %s が空", file)
	}
	return owner, nil
}

// writeRootOwner は scope key を書く。一時 file に書き切ってから link で置くので、読み手に書きかけを見せず、途中で落ちても空の
// file を残さない。link は既にあれば失敗するので、同時に撃った書き手のうち 1 つだけが置ける。先を越されたら何もしない。
func writeRootOwner(root, scopeKey string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("workspace root を作れない: %w", err)
	}
	tmp, err := os.CreateTemp(root, scopeKeyFile+".*.tmp")
	if err != nil {
		return fmt.Errorf("workspace root の scope key を書けない: %w", err)
	}
	defer os.Remove(tmp.Name())
	// CreateTemp は 0600 で作るので、他の利用者も読める 0644 にする。link の前に Sync し、電源断で空の file を残さない
	_, err = tmp.WriteString(scopeKey + "\n")
	err = errors.Join(err, tmp.Chmod(0o644), tmp.Sync(), tmp.Close())
	if err == nil {
		err = os.Link(tmp.Name(), filepath.Join(root, scopeKeyFile))
	}
	if err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("workspace root の scope key を書けない: %w", err)
	}
	return nil
}
