package coord_test

import (
	"testing"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/coord"
)

// 次の待ちは置き直しを始める前に掛ける。置き直しにかかった時間で間隔を延ばさない (system.md §9)。
func TestKeepBeatingArmsTheNextWaitBeforeTheHeartbeat(t *testing.T) {
	armed := make(chan struct{}, 4)
	clock := make(chan time.Time)
	inHeartbeat, release := make(chan struct{}), make(chan struct{})
	after := func(time.Duration) <-chan time.Time {
		armed <- struct{}{}
		return clock
	}
	heartbeat := func(time.Duration) error {
		inHeartbeat <- struct{}{}
		<-release
		return nil
	}
	stop := coord.KeepBeating(heartbeat, time.Minute, after, func(error) {})
	<-armed
	clock <- time.Time{}
	<-inHeartbeat
	select {
	case <-armed:
	default:
		t.Fatal("置き直しの最中に次の待ちが掛かっていない")
	}
	close(release)
	stop()
}
