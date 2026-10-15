package worker

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/printable"
)

// lineHeadLimit は活動と progress を判定するために持つ行の頭の大きさ。progress は progressLineLimit 以下の行の、活動は末尾の窓
// (tailLimit) に収まる行の中身を読むので、その大きい方
const lineHeadLimit = max(progressLineLimit, tailLimit)

// readChunk は stream の file から 1 回に読み出す大きさ
const readChunk = 64 << 10

// streamHandle は、loop が claude に渡した worker log の fd (またはその dup) に求める操作 (formats.md §6)。path を開き直す
// 操作は持たない。本番は *os.File、test は読めない状況を作る fake。
type streamHandle interface {
	Stat() (os.FileInfo, error)
	io.ReaderAt
}

// limits は stall と上限時間の上限。0 なら当てない。
type limits struct {
	stall, run time.Duration
}

// streamWatch は 1 attempt の stream を fd で読み、活動・progress の時計・止める理由を判定し、終わった後に end の要約を作る
// (formats.md §4 / §6)。「読めたときだけ時計と offset を進める」はこの型の中だけで守る。
type streamWatch struct {
	file streamHandle
	// origin は attempt を始めたときの stream の大きさ (今の attempt が書く行の頭)
	origin int64
	// lines は確認をまたいで stream を読み進める
	lines *lineReader
	limits
	// started は attempt を見始めた時刻 (上限時間の起点)
	started time.Time
	// progressed は progress を最後に見た時刻。まだ見ていなければ started
	progressed time.Time
	// readError は直近に stream を読めなかった理由。読めれば ""
	readError string
	// workspace は worker の workspace の path (活動の tool の入力の path を相対にする)
	workspace string
}

// observation は 1 回の確認の結果。
type observation struct {
	// activity は変わった活動。changed が false なら ""
	activity string
	changed  bool
	// stop は止める理由 (stall・上限時間)。止めなければ ""
	stop string
}

// newStreamWatch は、file の offset より後 (今の attempt が書く行) を started から見始める streamWatch。
func newStreamWatch(file streamHandle, offset int64, started time.Time, workspace string, l limits) *streamWatch {
	return &streamWatch{file: file, origin: offset, lines: newLineReader(offset, lineHeadLimit), limits: l, started: started,
		progressed: started, workspace: workspace}
}

// check は now の時点で stream を確かめ、活動が変わったかと止める理由を返す。前の確認から書かれた行に progress があれば
// stall の時計を now に戻す。読めなければ (大きさを確かめられない・中身を読み出せない のどちらも)、時計も読み終えた offset も
// 進めず、その理由を活動にする。読めなかった後は、伸びていなくても読み直す。
func (w *streamWatch) check(now time.Time) observation {
	activity, changed := w.read(now)
	return observation{activity: activity, changed: changed, stop: w.stopReason(now)}
}

func (w *streamWatch) read(now time.Time) (activity string, changed bool) {
	info, err := w.file.Stat()
	if err != nil {
		return w.unreadable(err)
	}
	size := info.Size()
	if size == w.lines.read && w.readError == "" {
		return "", false
	}
	// 前の確認から完結した行を古い順に見る。活動は file の末尾に収まる行のうち、数える最後の行
	var found, summarized bool
	var summary string
	err = w.lines.readTo(w.file, size, func(head []byte, start, length int64) {
		found = found || countsAsProgress(head, length)
		if !inTail(start, size) {
			return
		}
		if s, ok := Summarize(head, w.workspace); ok {
			summary, summarized = s, true
		}
	})
	// 読めなければ、それまでに見た行の結果は捨てる (readTo は読み終えた offset を戻し、次の確認で読み直す)
	if err != nil {
		return w.unreadable(err)
	}
	w.readError = ""
	if found {
		w.progressed = now
	}
	return summary, summarized
}

// unreadable は stream を読めなかった理由を活動にする。直前と同じ理由なら changed が false。
func (w *streamWatch) unreadable(err error) (activity string, changed bool) {
	message := printable.Line("stream を読めない: " + err.Error())
	if message == w.readError {
		return "", false
	}
	w.readError = message
	return message, true
}

// sinceProgress は progress を最後に見てから now までの時間。
func (w *streamWatch) sinceProgress(now time.Time) time.Duration { return now.Sub(w.progressed) }

// stopReason は now の時点で stall か上限時間に当たったら、その理由を返す。読めないあいだは progress を見られないので、stall の
// 理由に読めない理由を添える。
func (w *streamWatch) stopReason(now time.Time) string {
	quiet, running := w.sinceProgress(now), now.Sub(w.started)
	switch {
	case w.stall > 0 && quiet > w.stall && w.readError != "":
		return fmt.Sprintf("stall (progress が %s 途絶えた) · %s", w.stall, w.readError)
	case w.stall > 0 && quiet > w.stall:
		return fmt.Sprintf("stall (progress が %s 途絶えた)", w.stall)
	case w.run > 0 && running > w.run:
		return fmt.Sprintf("上限時間 %s を超えた", w.run)
	}
	return ""
}

// lastResult は attempt の最後の result の行を要約する (formats.md §4)。attempt 開始時の offset から読み、終了時の大きさが
// その offset より小さければ (attempt の途中で縮んだ) 0 から読む。行の切り方は監視と同じで、要約は行全体を読む。
func (w *streamWatch) lastResult() (*ResultSummary, error) {
	info, err := w.file.Stat()
	if err != nil {
		return nil, err
	}
	from := w.origin
	if info.Size() < from {
		from = 0
	}
	var last []byte
	if err := newLineReader(from, math.MaxInt).readTo(w.file, info.Size(), func(line []byte, _, _ int64) {
		if isResult(line) {
			last = append(last[:0], line...)
		}
	}); err != nil {
		return nil, err
	}
	if last == nil {
		return nil, nil
	}
	return summarizeResult(last)
}

// lineReader は stream を、確認をまたいで前に読み終えた offset から読み進め、完結した行 (改行で終わる行) を 1 行ずつ渡す。
// 書きかけの行は頭 (headLimit まで) と長さを持ち越し、次の確認で続きだけを読む。buffer は確認をまたいで使い回す。
type lineReader struct {
	// read は読み終えた offset。lineStart は書きかけの行の頭の offset (書きかけの行が無ければ read と同じ)
	read, lineStart int64
	// line は書きかけの行の頭 (headLimit まで)
	line      []byte
	headLimit int
	// chunk は読み出しに使う buffer。確認をまたいで使い回す
	chunk []byte
}

// newLineReader は offset より後を読み、行の頭を headLimit まで持つ lineReader。
func newLineReader(offset int64, headLimit int) *lineReader {
	return &lineReader{read: offset, lineStart: offset, headLimit: headLimit, line: make([]byte, 0, min(headLimit, readChunk)),
		chunk: make([]byte, readChunk)}
}

// readTo は file の size までのうち、まだ読んでいない byte を読み、完結した行ごとに each を呼ぶ。head は行の頭 (改行を含めて
// headLimit まで)、start は行の頭の offset、length は改行を含めた行の長さ。size が読み終えた offset より小さければ縮んだと
// みなし、頭から読む。読めなければ、この呼び出しで読んだ分を進めずに error を返す (呼び出し側は、それまでに呼んだ each の結果を
// 捨てる)。縮んだことは読めなくても覚えておく。
func (r *lineReader) readTo(file io.ReaderAt, size int64, each func(head []byte, start, length int64)) error {
	if size < r.read {
		r.read, r.lineStart, r.line = 0, 0, r.line[:0]
	}
	before := r.lineStart
	if err := r.readLines(file, size, each); err != nil {
		r.read, r.lineStart, r.line = before, before, r.line[:0]
		return err
	}
	return nil
}

func (r *lineReader) readLines(file io.ReaderAt, size int64, each func(head []byte, start, length int64)) error {
	for r.read < size {
		n, err := file.ReadAt(r.chunk[:min(int64(len(r.chunk)), size-r.read)], r.read)
		r.split(r.chunk[:n], each)
		switch {
		// 大きさを確かめてから縮んだ。縮んだ後の行は次の確認で頭から読む
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return err
		}
	}
	return nil
}

// split は読んだ byte を行に切り、完結した行ごとに each を呼ぶ。
func (r *lineReader) split(p []byte, each func(head []byte, start, length int64)) {
	for len(p) > 0 {
		part := p
		if i := bytes.IndexByte(p, '\n'); i >= 0 {
			part = p[:i+1]
		}
		r.line = append(r.line, part[:min(len(part), r.headLimit-len(r.line))]...)
		r.read += int64(len(part))
		p = p[len(part):]
		if part[len(part)-1] == '\n' {
			each(r.line, r.lineStart, r.read-r.lineStart)
			r.lineStart, r.line = r.read, r.line[:0]
		}
	}
}
