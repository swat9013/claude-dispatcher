// Package coordtest は coord.Board のテストが使う、coordination board の in-memory の adapter を持つ (system.md §13)。
package coordtest

import (
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/coord"
	"github.com/swat9013/claude-dispatcher/internal/target"
)

// Memory は ref と条件付きの更新だけを持つ、in-memory の coord.Refs。着手宣言と生存報告の ref を持ち、条件付きの更新
// (無いときだけ作る・旧 sha を期待値に置き換える・sha を指しているときだけ消す) を server と同じく照合する。テストが注入する
// 失敗 (FailClaim・FailClaimBeforePush・FailDelete・FailHeartbeat・FailLeave・Unreadable) も持つ。生存報告は別の goroutine から置き直すので、
// 表と注入した失敗は mu で守る。
type Memory struct {
	loop string

	mu       sync.Mutex
	claims   map[target.Ref]string
	loops    map[string]string
	messages map[string]coord.Message
	n        int
	// heartbeat は自分が最後に置けた生存報告の sha。無ければ ""
	heartbeat string

	// claimErr・deleteErr・heartbeatErr・leaveErr は注入した失敗 (FailClaim・FailDelete・FailHeartbeat・FailLeave)。nil なら落とさない
	claimErr, deleteErr, heartbeatErr, leaveErr error
	// claimUnpushedErr は注入した push より前の失敗 (FailClaimBeforePush)。nil なら落とさない
	claimUnpushedErr error
	// unread の sha の commit message は、読めなかったものとして View から外し、View.Unread に unreadErr を置く (Unreadable)
	unread    map[string]bool
	unreadErr error
}

var _ coord.Refs = (*Memory)(nil)

// board は ref の名前の綴りを引く coordination board の remote。綴りは本番の coord.Remote だけが持つ。
var board = coord.Remote{ScopeDir: "coordtest"}

// ClaimName は Memory が View に載せる、作業対象 ref の着手宣言の ref の名前。
func ClaimName(ref target.Ref) string { return board.ClaimRef(ref) }

// HeartbeatName は Memory が View に載せる、loop の生存報告の ref の名前。
func HeartbeatName(loop string) string { return board.HeartbeatRef(loop) }

// NewMemory は loop 名 loop の持ち主として coordination board を読み書きする、空の Memory を作る。
func NewMemory(loop string) *Memory {
	return &Memory{loop: loop, claims: map[target.Ref]string{}, loops: map[string]string{}, messages: map[string]coord.Message{}}
}

// FailClaim は以後の着手宣言の push を通したうえで err を返させる (結果の分からない失敗)。nil を渡すと戻す。
func (m *Memory) FailClaim(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claimErr = err
}

// FailClaimBeforePush は以後の着手宣言を、push せずに coord.ErrNotPushed と err を包んだ error で落とす (remote に届いて
// いない失敗)。nil を渡すと戻す。
func (m *Memory) FailClaimBeforePush(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claimUnpushedErr = err
}

// FailDelete は以後の着手宣言と生存報告の削除を、ref を変えずに err で落とす (負けではない失敗)。nil を渡すと戻す。
func (m *Memory) FailDelete(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteErr = err
}

// FailHeartbeat は以後の生存報告の置き直しを、ref を変えずに err で落とす。nil を渡すと戻す。
func (m *Memory) FailHeartbeat(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.heartbeatErr = err
}

// FailLeave は以後の Leave を、生存報告を消さずに err で落とす。nil を渡すと戻す。
func (m *Memory) FailLeave(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.leaveErr = err
}

// Unreadable は以後の Observe で、shas の commit message を読めなかったものとして View から外し、外したものがあれば
// View.Unread に err を置く。呼ぶたびに前の指定を置き換え、shas を渡さなければ戻す。
func (m *Memory) Unreadable(err error, shas ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unread, m.unreadErr = map[string]bool{}, err
	for _, sha := range shas {
		m.unread[sha] = true
	}
}

// Put は他の loop の着手宣言か生存報告を置き、置いた commit の sha を返す。ref が nil なら msg.Loop の生存報告、nil でなければ
// ref の着手宣言を、条件を付けずに置き換える。注入した失敗は効かない。
func (m *Memory) Put(ref *target.Ref, msg coord.Message) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.put(ref, msg)
}

func (m *Memory) put(ref *target.Ref, msg coord.Message) string {
	m.n++
	sha := fmt.Sprintf("%s#%d", msg.Loop, m.n)
	m.messages[sha] = msg
	if ref != nil {
		m.claims[*ref] = sha
	} else {
		m.loops[msg.Loop] = sha
	}
	return sha
}

// Claims は今の着手宣言の作業対象ごとの sha (写し)。
func (m *Memory) Claims() map[target.Ref]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.claims)
}

// Loops は今の生存報告の loop 名ごとの sha (写し)。
func (m *Memory) Loops() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.loops)
}

// Message は sha の commit が持つ commit message。
func (m *Memory) Message(sha string) coord.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.messages[sha]
}

func (m *Memory) LoopName() string { return m.loop }

func (m *Memory) Observe() (coord.View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := coord.View{
		Claims: map[target.Ref]coord.RefState{}, Loops: map[string]coord.RefState{}, Messages: map[string]coord.Message{},
	}
	for ref, sha := range m.claims {
		v.Claims[ref], v.Messages[sha] = coord.RefState{SHA: sha, Name: ClaimName(ref)}, m.messages[sha]
	}
	for loop, sha := range m.loops {
		v.Loops[loop], v.Messages[sha] = coord.RefState{SHA: sha, Name: HeartbeatName(loop)}, m.messages[sha]
	}
	for sha := range m.unread {
		if _, ok := v.Messages[sha]; ok {
			delete(v.Messages, sha)
			v.Unread = m.unreadErr
		}
	}
	return v, nil
}

func (m *Memory) Claim(ref target.Ref, expect coord.Expect) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claimUnpushedErr != nil {
		// 本番 (coord.Party) と同じく、commit を作れなければ期待値を照合する push まで行かない
		return "", fmt.Errorf("%w: %w", coord.ErrNotPushed, m.claimUnpushedErr)
	}
	if expect.String() != expected(m.claims[ref]) {
		return "", coord.ErrLost
	}
	sha := m.put(&ref, coord.Message{Kind: coord.KindClaim, Format: coord.Format, Loop: m.loop, Target: ref.String()})
	if m.claimErr != nil {
		return "", m.claimErr
	}
	return sha, nil
}

func (m *Memory) DeleteClaim(ref target.Ref, sha string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return m.deleteErr
	}
	if current, ok := m.claims[ref]; !ok || current != sha {
		return coord.ErrLost
	}
	delete(m.claims, ref)
	return nil
}

func (m *Memory) DeleteHeartbeat(loop, sha string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return m.deleteErr
	}
	if current, ok := m.loops[loop]; !ok || current != sha {
		return coord.ErrLost
	}
	delete(m.loops, loop)
	return nil
}

func (m *Memory) Heartbeat(interval time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.heartbeatErr != nil {
		return m.heartbeatErr
	}
	m.heartbeat = m.put(nil, coord.Message{Kind: coord.KindHeartbeat, Format: coord.Format, Loop: m.loop, Interval: interval})
	return nil
}

// Leave は自分が最後に置けた生存報告を、その sha を指しているときだけ消す。置けていなければ何もしない。
func (m *Memory) Leave() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.leaveErr != nil {
		return m.leaveErr
	}
	if m.heartbeat == "" {
		return nil
	}
	if m.loops[m.loop] != m.heartbeat {
		return coord.ErrLost
	}
	delete(m.loops, m.loop)
	m.heartbeat = ""
	return nil
}

// expected は ref が今 current の sha を指しているときに通る期待値の綴り (coord.Expect.String)。
func expected(current string) string {
	if current == "" {
		return "absent"
	}
	return "holding " + current
}
