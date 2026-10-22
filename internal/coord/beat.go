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

// Failures は生存報告の置き直しが続けて落ちた回数と、直近の理由 (状態 file の heartbeat_failures の元)。単発起動は数を出す先が無いので数えない。
type Failures struct {
	Count int
	Last  error
}

// Note は置き直しの結果を数える。落ちていれば続けて落ちた回数を足し、通っていれば 0 に戻す。
func (f *Failures) Note(err error) {
	if err != nil {
		f.Count, f.Last = f.Count+1, err
		return
	}
	f.Count, f.Last = 0, nil
}
