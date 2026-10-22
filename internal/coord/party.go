package coord

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/target"
)

// Party は 1 つの loop (か単発起動) として調停 remote に着手宣言と生存報告を置く口。読んだ commit message を sha ごとに
// 覚え、新しく見た sha だけを fetch する。生存報告は tick と別の goroutine から置き直すので、記憶は mu で守る。
type Party struct {
	Remote Remote
	// Loop は自分の loop 名
	Loop string

	mu sync.Mutex
	// seq は直近に置こうとした (置けたとは限らない) 生存報告の seq
	seq int
	// heartbeat は直近に置けた生存報告の sha。無ければ ""
	heartbeat string
	messages  map[string]Message
}

// View は調停 remote の scope dir の下の ref を、読んだ形にしたもの。
type View struct {
	// Claims は着手宣言の作業対象ごとの sha
	Claims map[target.Ref]string
	// Loops は生存報告の loop 名ごとの sha
	Loops map[string]string
	// Messages は読めた commit message。読めなかった sha は無い
	Messages map[string]Message
	// Unread は commit message を読めなかった理由。処理は続ける
	Unread error
}

// Newer は、自分の知る版より新しい版 (版を読めないものを含む) の commit message があればそれを返す。
func (v View) Newer() (Message, bool) {
	for _, m := range v.Messages {
		if m.Newer() {
			return m, true
		}
	}
	return Message{}, false
}

// LoopName は自分の loop 名。
func (p *Party) LoopName() string { return p.Loop }

// ClaimRef は作業対象の着手宣言の ref の名前。
func (p *Party) ClaimRef(ref target.Ref) string { return p.Remote.ClaimRef(ref) }

// HeartbeatRef は loop 名の生存報告の ref の名前。
func (p *Party) HeartbeatRef(loop string) string { return p.Remote.HeartbeatRef(loop) }

// remember は sha の commit message を覚える。
func (p *Party) remember(sha string, m Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.messages == nil {
		p.messages = map[string]Message{}
	}
	p.messages[sha] = m
}

// Observe は ref を読み、新しく見た sha の commit message を読む。覚える message は今の ref が指す sha のものだけに刈る。
// ref を読めなければ error。
func (p *Party) Observe() (View, error) {
	refs, err := p.Remote.List()
	if err != nil {
		return View{}, fmt.Errorf("調停 remote %s の ref を読めない: %w", p.Remote.Name, err)
	}
	v := View{Claims: map[target.Ref]string{}, Loops: map[string]string{}, Messages: map[string]Message{}}
	claims, loops := p.Remote.prefix()+"claims/", p.Remote.prefix()+"loops/"
	listed := map[string]bool{}
	for name, sha := range refs {
		switch {
		case strings.HasPrefix(name, claims):
			ref, ok := target.ParseFileName(strings.TrimPrefix(name, claims))
			if !ok {
				continue
			}
			v.Claims[ref] = sha
		case strings.HasPrefix(name, loops):
			v.Loops[strings.TrimPrefix(name, loops)] = sha
		default:
			continue
		}
		listed[sha] = true
	}
	p.mu.Lock()
	kept := map[string]Message{}
	var unknown []string
	for sha := range listed {
		if m, read := p.messages[sha]; read {
			kept[sha] = m
		} else {
			unknown = append(unknown, sha)
		}
	}
	// 置き直したばかりの自分の生存報告は、次の Observe まで ref に載らないことがあるので刈らない
	if m, ok := p.messages[p.heartbeat]; ok {
		kept[p.heartbeat] = m
	}
	p.messages = kept
	p.mu.Unlock()
	read, err := p.Remote.Read(unknown)
	for sha, m := range read {
		p.remember(sha, m)
	}
	p.mu.Lock()
	for sha := range listed {
		if m, ok := p.messages[sha]; ok {
			v.Messages[sha] = m
		}
	}
	p.mu.Unlock()
	v.Unread = err
	return v, nil
}

// Claim は作業対象の着手宣言を、expect を条件にして置き、置いた sha を返す。負けたら ErrLost を包んで返す。
func (p *Party) Claim(ref target.Ref, expect Expect) (string, error) {
	m := Message{Kind: KindClaim, Format: Format, Loop: p.Loop, Target: ref.String()}
	sha, err := p.Remote.Commit(m)
	if err != nil {
		return "", fmt.Errorf("着手宣言の commit を作れない: %w", err)
	}
	if err := p.Remote.Push(p.ClaimRef(ref), sha, expect); err != nil {
		return "", err
	}
	p.remember(sha, m)
	return sha, nil
}

// Delete は ref を、sha を指しているときだけ消す。負けたら ErrLost を包んで返す。
func (p *Party) Delete(name, sha string) error { return p.Remote.Push(name, "", Holding(sha)) }

// Heartbeat は生存報告を、間隔 interval を書いた新しい commit で置き直す。条件は付けない (書くのは自分だけ)。
func (p *Party) Heartbeat(interval time.Duration) error {
	p.mu.Lock()
	p.seq++
	m := Message{Kind: KindHeartbeat, Format: Format, Loop: p.Loop, Interval: interval, Seq: p.seq}
	p.mu.Unlock()
	sha, err := p.Remote.Commit(m)
	if err != nil {
		return fmt.Errorf("生存報告の commit を作れない: %w", err)
	}
	if err := p.Remote.Push(p.HeartbeatRef(p.Loop), sha, Unconditional()); err != nil {
		return err
	}
	p.remember(sha, m)
	p.mu.Lock()
	p.heartbeat = sha
	p.mu.Unlock()
	return nil
}

// Leave は自分の生存報告を消す。置けていなければ何もしない。生存報告の置き直しを止めてから呼ぶ。
func (p *Party) Leave() error {
	p.mu.Lock()
	sha := p.heartbeat
	p.mu.Unlock()
	if sha == "" {
		return nil
	}
	err := p.Delete(p.HeartbeatRef(p.Loop), sha)
	if err == nil {
		p.mu.Lock()
		p.heartbeat = ""
		p.mu.Unlock()
	}
	return err
}

// HeartbeatInterval は生存報告の間隔: tick の周期と 1 分の長いほう (system.md §9)。
func HeartbeatInterval(tick time.Duration) time.Duration { return max(tick, time.Minute) }
