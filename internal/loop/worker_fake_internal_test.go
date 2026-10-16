package loop

import (
	"sync"

	"github.com/swat9013/claude-dispatcher/internal/status"
)

// FakeWorker は loop の test が使う唯一の worker の fake。package loop の test と loop_test の両方から使えるよう、
// ここで export する。Activity は Act を返し、Stop は 1 回目だけ OnStop を呼ぶ。
type FakeWorker struct {
	// Act は Activity が返す活動
	Act status.Activity
	// OnStop は Stop の 1 回目に呼ぶもの (nil なら何もしない)
	OnStop func()
	once   sync.Once
}

func (w *FakeWorker) Stop() {
	w.once.Do(func() {
		if w.OnStop != nil {
			w.OnStop()
		}
	})
}

func (w *FakeWorker) Activity() status.Activity { return w.Act }
