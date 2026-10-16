package worker

import (
	"os"
	"strings"
	"testing"
	"time"
)

// 確認をまたいで書かれる行の読み方 (formats.md §6 の活動と progress)。1 行が何回かの確認にまたがって書かれても、完結した
// ときに 1 行として判定する。

func TestALineWrittenAcrossChecksResetsTheStallClockOnceItIsComplete(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	line := text("分けて書く")
	appendAt(t, w, line[:10], started.Add(10*time.Second))
	appendAt(t, w, line[10:20], started.Add(20*time.Second))

	appendAt(t, w, line[20:], started.Add(30*time.Second))

	if got := w.sinceProgress(started.Add(30 * time.Second)); got != 0 {
		t.Fatalf("progress が途絶えた時間 = %s, want 完結した確認で時計が戻って 0s", got)
	}
}

func TestALineWrittenAcrossChecksDoesNotResetTheStallClockBeforeItIsComplete(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	line := text("分けて書く")
	appendAt(t, w, line[:10], started.Add(10*time.Second))

	appendAt(t, w, line[10:len(line)-1], started.Add(20*time.Second))

	if got := w.sinceProgress(started.Add(20 * time.Second)); got != 20*time.Second {
		t.Fatalf("progress が途絶えた時間 = %s, want 完結するまで戻らず 20s", got)
	}
}

func TestALineWrittenAcrossChecksBecomesTheActivityOnceItIsComplete(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	line := text("分けて書く")
	appendAt(t, w, line[:10], started.Add(10*time.Second))
	write(t, w, line[10:])

	activity, changed := w.check(started.Add(20 * time.Second))

	if !changed || activity != "分けて書く" {
		t.Fatalf("活動 = %q, %v; want 分けて書く", activity, changed)
	}
}

func TestALongLineWrittenAcrossChecksCountsAsProgressWhateverItsType(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	line := heartbeatPadded(t, progressLineLimit+1)
	appendAt(t, w, line[:progressLineLimit/2], started.Add(10*time.Second))

	appendAt(t, w, line[progressLineLimit/2:], started.Add(20*time.Second))

	if got := w.sinceProgress(started.Add(20 * time.Second)); got != 0 {
		t.Fatalf("progress が途絶えた時間 = %s, want 64 KiB を超える行は分けて書かれても type を見ずに数えて 0s", got)
	}
}

func TestALineHalfWrittenWhenTheStreamCouldNotBeReadIsJudgedWholeOnceItCanBeRead(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	line := text("分けて書く")
	appendAt(t, w, line[:10], started.Add(10*time.Second))
	restore := failReads(w)
	w.check(started.Add(20 * time.Second))
	restore()
	write(t, w, line[10:])

	activity, changed := w.check(started.Add(30 * time.Second))

	if !changed || activity != "分けて書く" {
		t.Fatalf("活動 = %q, %v; want 読めなかった前後に分けて書かれた行を 1 行として 分けて書く", activity, changed)
	}
}

func TestAHalfWrittenLineIsDroppedWhenTheStreamShrinks(t *testing.T) {
	started := time.Now()
	w := stallClock(t, started, time.Minute)
	// 書きかけの頭は書き直した後の大きさより大きいので、縮んだとみなせる
	appendAt(t, w, text(strings.Repeat("前", 100))[:200], started.Add(10*time.Second))
	shrinkTo(t, w, text("後"))

	activity, changed := w.check(started.Add(20 * time.Second))

	if !changed || activity != "後" {
		t.Fatalf("活動 = %q, %v; want 縮む前の書きかけの頭を捨てて 後", activity, changed)
	}
}

// longLineSize は計測で小分けに書く 1 行の大きさ、piece は 1 回に書く大きさ
const (
	longLineSize = 4 << 20
	piece        = 16 << 10
)

// BenchmarkCheckWhileALongLineIsWrittenInPieces は、longLineSize の 1 行を piece ずつ書き、書くたびに確かめる。1 op は
// 行を書き終えるまでの longLineSize/piece 回の書き込みと確認。
func BenchmarkCheckWhileALongLineIsWrittenInPieces(b *testing.B) {
	line := text(strings.Repeat("x", longLineSize-len(text(""))))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		file := b.TempDir() + "/stream.log"
		if err := os.WriteFile(file, nil, 0o644); err != nil {
			b.Fatal(err)
		}
		f, err := openStream(file)
		if err != nil {
			b.Fatal(err)
		}
		started := time.Now()
		w := newStreamWatch(f, 0, started, "/work", limits{})
		b.StartTimer()
		for written := 0; written < len(line); written += piece {
			if _, err := f.WriteString(line[written:min(written+piece, len(line))]); err != nil {
				b.Fatal(err)
			}
			w.check(started.Add(time.Second))
		}
		b.StopTimer()
		f.Close()
		if w.sinceProgress(started.Add(time.Second)) != 0 {
			b.Fatal("書き終えた行を progress と数えていない")
		}
		b.StartTimer()
	}
}
