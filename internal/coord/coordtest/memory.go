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
// (無いときだけ作る・旧 sha を期待値に置き換える・sha を指しているときだけ消す) を server と同じく照合する。生存報告は
// 別の goroutine から置き直すので、表は mu で守る。
type Memory struct {
	loop string

	mu       sync.Mutex
	claims   map[target.Ref]string
	loops    map[string]string
	messages map[string]coord.Message
	n        int
}

var _ coord.Refs = (*Memory)(nil)

// NewMemory は loop 名 loop の持ち主として coordination board を読み書きする、空の Memory を作る。
func NewMemory(loop string) *Memory {
	return &Memory{loop: loop, claims: map[target.Ref]string{}, loops: map[string]string{}, messages: map[string]coord.Message{}}
}

// Put は他の loop の着手宣言か生存報告を置き、置いた commit の sha を返す。ref が nil なら msg.Loop の生存報告、nil でなければ
// ref の着手宣言を、条件を付けずに置き換える。
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
		v.Claims[ref], v.Messages[sha] = coord.RefState{SHA: sha, Name: "claims/" + ref.FileName()}, m.messages[sha]
	}
	for loop, sha := range m.loops {
		v.Loops[loop], v.Messages[sha] = coord.RefState{SHA: sha, Name: "loops/" + loop}, m.messages[sha]
	}
	return v, nil
}

func (m *Memory) Claim(ref target.Ref, expect coord.Expect) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if expect.String() != expected(m.claims[ref]) {
		return "", coord.ErrLost
	}
	return m.put(&ref, coord.Message{Kind: coord.KindClaim, Format: coord.Format, Loop: m.loop, Target: ref.String()}), nil
}

func (m *Memory) DeleteClaim(ref target.Ref, sha string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.claims[ref]; !ok || current != sha {
		return coord.ErrLost
	}
	delete(m.claims, ref)
	return nil
}

func (m *Memory) DeleteHeartbeat(loop, sha string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.loops[loop]; !ok || current != sha {
		return coord.ErrLost
	}
	delete(m.loops, loop)
	return nil
}

func (m *Memory) Heartbeat(interval time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.put(nil, coord.Message{Kind: coord.KindHeartbeat, Format: coord.Format, Loop: m.loop, Interval: interval})
	return nil
}

func (m *Memory) Leave() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.loops, m.loop)
	return nil
}

// expected は ref が今 current の sha を指しているときに通る期待値の綴り (coord.Expect.String)。
func expected(current string) string {
	if current == "" {
		return "absent"
	}
	return "holding " + current
}
