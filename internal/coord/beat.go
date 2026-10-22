package coord

import "time"

// KeepBeating は、止めるまで別の goroutine で heartbeat を interval ごとに撃ち、結果を report に渡す (system.md §9 の生存報告の
// 置き直し)。after は待つ時計 (本番は time.After)。返した関数で止め、止まるまで待つ。loop と単発起動が共に使う。
func KeepBeating(heartbeat func(time.Duration) error, interval time.Duration, after func(time.Duration) <-chan time.Time, report func(error)) (stop func()) {
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		// 次の待ちは置き直しを始める前に掛け、置き直しにかかった時間で間隔を延ばさない
		next := after(interval)
		for {
			select {
			case <-quit:
				return
			case <-next:
			}
			next = after(interval)
			report(heartbeat(interval))
		}
	}()
	return func() {
		close(quit)
		<-done
	}
}
