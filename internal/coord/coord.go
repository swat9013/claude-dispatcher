// Package coord は調停 remote の部品 (system.md §4 の「マシンを跨いだ分担」・§13、formats.md §12)。実装 repo の git
// remote の `refs/dispatcher/<scope dir>/` の下に、着手宣言と生存報告を git で置き・読み・消す。時計は持たず、死んだ
// loop の判定は呼び出し側 (loop) が行う。
package coord

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/proc"
	"github.com/swat9013/claude-dispatcher/internal/state"
	"github.com/swat9013/claude-dispatcher/internal/target"
)

// Format は、この binary が書き、読める commit message の形式の版 (ADR 0023)
const Format = 1

// commit message の 1 行目 (formats.md §12)
const (
	KindClaim     = "claude-dispatcher claim"
	KindHeartbeat = "claude-dispatcher heartbeat"
)

// ErrLost は条件付きの push に負けたこと (formats.md §12 の「負け」)。
var ErrLost = errors.New("条件付きの push に負けた")

// Message は着手宣言か生存報告の commit message。Format が 0 なら版を読めなかった。
type Message struct {
	Kind     string
	Format   int
	Loop     string
	Target   string
	Interval time.Duration
	Seq      int
}

// Newer は、自分の知る版より新しい版 (版を読めないものを含む) の ref か。
func (m Message) Newer() bool { return m.Format <= 0 || m.Format > Format }

// String は commit message の綴り。
func (m Message) String() string {
	lines := []string{m.Kind, "", fmt.Sprintf("format: %d", m.Format), "loop: " + m.Loop}
	if m.Target != "" {
		lines = append(lines, "target: "+m.Target)
	}
	if m.Interval > 0 {
		lines = append(lines, "interval: "+m.Interval.String(), fmt.Sprintf("seq: %d", m.Seq))
	}
	return strings.Join(lines, "\n") + "\n"
}

// ParseCommit は `git cat-file commit` の出力から commit message を読む。知らない key は読み飛ばす。
func ParseCommit(raw string) Message {
	_, body, _ := strings.Cut(raw, "\n\n")
	head, rest, _ := strings.Cut(body, "\n")
	m := Message{Kind: strings.TrimSpace(head)}
	for _, line := range strings.Split(rest, "\n") {
		key, value, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "format":
			if n, err := strconv.Atoi(value); err == nil {
				m.Format = n
			}
		case "loop":
			m.Loop = value
		case "target":
			m.Target = value
		case "interval":
			m.Interval, _ = time.ParseDuration(value)
		case "seq":
			m.Seq, _ = strconv.Atoi(value)
		}
	}
	return m
}

// NewLoopName は、この起動の loop 名: `<hostname を無害化したもの>-<乱数 8 hex>` (formats.md §12)。
func NewLoopName() (string, error) {
	host, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("loop 名に使う hostname を読めない: %w", err)
	}
	return newLoopName(host, rand.Read)
}

func newLoopName(host string, read func([]byte) (int, error)) (string, error) {
	b := make([]byte, 4)
	if _, err := read(b); err != nil {
		return "", fmt.Errorf("loop 名に使う乱数を作れない: %w", err)
	}
	return state.Sanitize(host) + "-" + hex.EncodeToString(b), nil
}

// Remote は、workflow 定義のある clone から撃つ git で調停 remote を読み書きする口。
type Remote struct {
	// Name は coordination.remote (clone の git remote の名前)
	Name string
	// ScopeDir は scope dir (formats.md §1)
	ScopeDir string
	// Git は git の呼び出し口。Dir は workflow 定義のある dir
	Git proc.Command
	// ssh は、利用者が ssh の撃ち方を決めているかの判定。読めた判定を持ち、以後は読み直さない (値は run の間変わらない)
	ssh *sshChoice
}

type sshChoice struct {
	mu    sync.Mutex
	known bool
	set   bool
}

// NewRemote は調停 remote の口を作る。ssh の撃ち方の判定 (core.sshCommand の読み出し) は、読めたらこの口とその写しの間で持ち、以後は読み直さない。
func NewRemote(name, scopeDir string, git proc.Command) Remote {
	return Remote{Name: name, ScopeDir: scopeDir, Git: git, ssh: &sshChoice{}}
}

// 作者と committer。利用者の git の設定に頼らない (formats.md §12)
var identity = []string{
	"GIT_AUTHOR_NAME=claude-dispatcher", "GIT_AUTHOR_EMAIL=claude-dispatcher@invalid",
	"GIT_COMMITTER_NAME=claude-dispatcher", "GIT_COMMITTER_EMAIL=claude-dispatcher@invalid",
}

func (r Remote) prefix() string { return "refs/dispatcher/" + r.ScopeDir + "/" }

// git は認証の prompt を出させない git の呼び出し口。loop は無人で回るので、prompt は答えられずに上限時間まで止まる。
// 利用者が ssh の撃ち方を決めていなければ、ssh の passphrase と host key の確認も出させない (system.md §13)。
func (r Remote) git(env ...string) proc.Command {
	git := r.Git
	git.Env = append(slices.Clone(git.Env), "GIT_TERMINAL_PROMPT=0")
	if !r.sshCommandIsSet(git) {
		git.Env = append(git.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	git.Env = append(git.Env, env...)
	return git
}

// sshCommandIsSet は、NewRemote で作った口では読めた判定を持ち、以後はそれを返す。設定を読めなかったときの判定は持たず、
// 次の呼び出しで読み直す (一時的な失敗で、run の間ずっと BatchMode を外さないため)。
func (r Remote) sshCommandIsSet(git proc.Command) bool {
	if r.ssh == nil {
		set, _ := readSSHCommandSetting(git)
		return set
	}
	r.ssh.mu.Lock()
	defer r.ssh.mu.Unlock()
	if !r.ssh.known {
		r.ssh.set, r.ssh.known = readSSHCommandSetting(git)
	}
	return r.ssh.set
}

// readSSHCommandSetting は、利用者が GIT_SSH_COMMAND・GIT_SSH・core.sshCommand で ssh の撃ち方を決めているかを返す。決めていれば
// その設定を上書きしない。設定を読めなかったときも、上書きしないほうへ倒す。known は、判定が設定を読めた結果か。
func readSSHCommandSetting(git proc.Command) (set, known bool) {
	for _, kv := range git.Env {
		// GIT_SSH は GIT_SSH_COMMAND に負けるので、足すと利用者の wrapper を上書きする
		if strings.HasPrefix(kv, "GIT_SSH_COMMAND=") || strings.HasPrefix(kv, "GIT_SSH=") {
			return true, true
		}
	}
	value, err := git.Output("config", "--get", "core.sshCommand")
	// 設定が無ければ git は exit 1 で何も出さない
	if pe := (*proc.Error)(nil); errors.As(err, &pe) && pe.Exit == 1 {
		return false, true
	}
	if err != nil {
		return true, false
	}
	return strings.TrimSpace(value) != "", true
}

// ClaimRef は作業対象の着手宣言の ref。
func (r Remote) ClaimRef(ref target.Ref) string { return r.prefix() + "claims/" + ref.FileName() }

// HeartbeatRef は loop の生存報告の ref。
func (r Remote) HeartbeatRef(loop string) string { return r.prefix() + "loops/" + loop }

// Confirm は起動時の調停 remote の確認 (formats.md §12): remote を解き、ref を読めることを確かめ、remote の URL を返す。
func (r Remote) Confirm() (string, error) {
	url, err := r.git().Output("remote", "get-url", "--all", r.Name)
	if err != nil {
		return "", fmt.Errorf("調停 remote %s を解けない: %w", r.Name, err)
	}
	push, err := r.git().Output("remote", "get-url", "--push", "--all", r.Name)
	if err != nil {
		return "", fmt.Errorf("調停 remote %s を解けない: %w", r.Name, err)
	}
	// 読む URL と書く URL が違うか複数あると、自分の置いた ref を読めないか、push の勝ち負けが読む URL の状態と食い違う
	url, push = strings.TrimSpace(url), strings.TrimSpace(push)
	if url != push || strings.Contains(url, "\n") {
		return "", fmt.Errorf("調停 remote %s の読む URL と書く URL が違う: %s と %s", r.Name, strings.ReplaceAll(url, "\n", ", "), strings.ReplaceAll(push, "\n", ", "))
	}
	if _, err := r.List(); err != nil {
		return "", fmt.Errorf("調停 remote %s を読めない: %w", r.Name, err)
	}
	return url, nil
}

// List は scope dir の下の ref を読み、ref の名前から sha への表を返す。
func (r Remote) List() (map[string]string, error) {
	out, err := r.git().Output("ls-remote", r.Name, r.prefix()+"*")
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		sha, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && strings.HasPrefix(name, r.prefix()) {
			refs[name] = sha
		}
	}
	return refs, nil
}

// Read は sha の commit を fetch して commit message を読む。読めなかった sha は表に入れず、理由をまとめて返す。
func (r Remote) Read(shas []string) (map[string]Message, error) {
	messages := map[string]Message{}
	if len(shas) == 0 {
		return messages, nil
	}
	args := append([]string{"fetch", "--no-tags", "--no-write-fetch-head", r.Name}, shas...)
	if _, err := r.git().Output(args...); err != nil {
		return messages, fmt.Errorf("調停 remote %s の commit を fetch できない: %w", r.Name, err)
	}
	var errs []error
	for _, sha := range shas {
		raw, err := r.git().Output("cat-file", "commit", sha)
		if err != nil {
			errs = append(errs, fmt.Errorf("調停 remote の commit %s を読めない: %w", sha, err))
			continue
		}
		messages[sha] = ParseCommit(raw)
	}
	return messages, errors.Join(errs...)
}

// Commit は m を commit message に持つ、空の tree の commit を作る。
func (r Remote) Commit(m Message) (string, error) {
	tree, err := r.git().OutputWithInput("", "mktree")
	if err != nil {
		return "", err
	}
	sha, err := r.git(identity...).Output("commit-tree", strings.TrimSpace(tree), "-m", m.String())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sha), nil
}

// Expect は条件付きの push の期待値。
type Expect struct {
	// unconditional なら条件を付けない
	unconditional bool
	// sha は remote の ref の期待する sha。"" なら ref が無いこと
	sha string
}

// Absent は「ref が無いときだけ」。
func Absent() Expect { return Expect{} }

// Holding は「ref が sha を指しているときだけ」。
func Holding(sha string) Expect { return Expect{sha: sha} }

// Unconditional は条件を付けない (生存報告。書くのは持ち主の loop だけ)。
func Unconditional() Expect { return Expect{unconditional: true} }

// String は期待値の綴り: `absent` / `holding <sha>` / `unconditional`。loop のテストの fake が期待値を照合するのに使う。
func (e Expect) String() string {
	switch {
	case e.unconditional:
		return "unconditional"
	case e.sha == "":
		return "absent"
	}
	return "holding " + e.sha
}

// Push は sha を ref へ push する。sha が "" なら ref を消す。負けたら ErrLost を包んで返す。push はすべて --no-verify で
// 撃ち、実装 repo の pre-push hook を通さない (ADR 0017)。
func (r Remote) Push(ref, sha string, expect Expect) error {
	args := []string{"push", "--no-verify", "--porcelain"}
	spec := sha + ":" + ref
	if expect.unconditional {
		spec = "+" + spec
	} else {
		args = append(args, "--force-with-lease="+ref+":"+expect.sha)
	}
	_, err := r.git().Output(append(args, r.Name, spec)...)
	var failure *proc.Error
	if errors.As(err, &failure) && lost(failure.Stdout) {
		return fmt.Errorf("%s: %w", ref, ErrLost)
	}
	return err
}

// lost は push の porcelain の出力が、`!` で始まり `[remote rejected]` か `[rejected]` を含む行を持つか。理由の文言は見ない
// (formats.md §12 の「負け」)。
func lost(stdout string) bool {
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "!") && (strings.Contains(line, "[remote rejected]") || strings.Contains(line, "[rejected]")) {
			return true
		}
	}
	return false
}
