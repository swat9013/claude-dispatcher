package coord

import "time"

// KeepBeating は、止めるまで別の goroutine で heartbeat を interval ごとに撃ち、結果を report に渡す (system.md §9 の生存報告の
// 置き直し)。after は待つ時計 (本番は time.After)。返した関数で止め、止まるまで待つ。loop と単発起動が共に使う。
func KeepBeating(heartbeat func(time.Duration) error, interval time.Duration, after func(time.Duration) <-chan time.Time, report func(error)) (stop func()) {
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-quit:
				return
			case <-after(interval):
			}
			report(heartbeat(interval))
		}
	}()
	return func() {
		close(quit)
		<-done
	}
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
