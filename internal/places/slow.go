package places

import (
	"fmt"
	"sync"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/target"
	"github.com/swat9013/claude-dispatcher/internal/workflow"
)

// SlowListing は 1 つの置き場の一覧を 1 回読む時間の目標。超えたら、読み終わる前に 1 行出す (formats.md §6 の「遅い一覧」)。
const SlowListing = 15 * time.Second

// SlowNotice は、一覧の読み出しが SlowListing を超えたことを、読み終わる前に読み出し 1 回につき 1 回だけ知らせる口。After か
// Notify が nil なら知らせない。Notify は見張りの goroutine で呼び、Watch (Store.Open も Watch を通る) は Notify が終わる
// まで返らない。Notify は同時には呼ばない (OpenItems が一覧を並べて読んでも 1 つずつ呼ぶ) ので、行の書き手は lock を要らない。
type SlowNotice struct {
	// After は d 後に届く channel を返す (time.After。テストは差し替える)
	After  func(d time.Duration) <-chan time.Time
	Notify func(kind target.Kind)
}

// Watch は read を呼び、read が SlowListing を超えたら、終わる前に kind の置き場の知らせを 1 回出す。After か Notify が nil
// なら read を呼ぶだけ。知らせても read は止めない。
func (n SlowNotice) Watch(kind target.Kind, read func()) {
	if n.After == nil || n.Notify == nil {
		read()
		return
	}
	// 読み出しは呼んだ goroutine のまま撃ち (panic も呼んだ側で起きる)、見張りだけを別の goroutine に置く。見張りが
	// 終わるまで返らないので、知らせの行は Watch の後に呼んだ側が出す行と並ばない
	finished, watching := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watching)
		select {
		case <-n.After(SlowListing):
			n.Notify(kind)
		case <-finished:
		}
	}()
	defer func() {
		close(finished)
		<-watching
	}()
	read()
}

// oneAtATime は、Notify を同時に呼ばない口。並べて読む一覧 (OpenItems) の知らせが、書き手の側で行を混ぜないようにする。
func (n SlowNotice) oneAtATime() SlowNotice {
	if n.Notify == nil {
		return n
	}
	var mu sync.Mutex
	notify := n.Notify
	n.Notify = func(kind target.Kind) {
		mu.Lock()
		defer mu.Unlock()
		notify(kind)
	}
	return n
}

// SlowListingLine は、kind の置き場の一覧の読み出しが SlowListing を超えたときに出す 1 行。
func SlowListingLine(def workflow.Definition, kind target.Kind) string {
	return fmt.Sprintf("%s の一覧の読み出しが %d 秒を超えた", Label(def, kind), int(SlowListing/time.Second))
}
