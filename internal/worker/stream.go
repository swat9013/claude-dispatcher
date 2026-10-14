package worker

import (
	"bytes"
	"errors"
	"io"
	"os"
)

// lineHeadLimit は完結した行を判定するために持つ行の頭の大きさ。progress と活動の、判定に中身を読む行の上限のうち大きい方
const lineHeadLimit = max(progressLineLimit, tailLimit)

// readChunk は stream の file から 1 回に読み出す大きさ
const readChunk = 64 << 10

// lineReader は stream の file を、確認をまたいで前に読み終えた offset から読み進め、完結した行 (改行で終わる行) を 1 行ずつ
// 渡す。書きかけの行は頭 (lineHeadLimit まで) と長さを持ち越し、次の確認で続きだけを読む。buffer は確認をまたいで使い回す。
type lineReader struct {
	// path は stream の file の path。確認のたびに path で開く (置き換えを縮みとして見る)
	path string
	// size は前に読み終えたときの file の大きさ。これより小さくなったら縮んだとみなす
	size int64
	// read は読み終えた offset。lineStart は書きかけの行の頭の offset (書きかけの行が無ければ read と同じ)
	read, lineStart int64
	// line は書きかけの行の頭 (lineHeadLimit まで)
	line []byte
	// chunk は読み出しに使う buffer。確認をまたいで使い回す
	chunk []byte
}

// newLineReader は path の stream の file の offset より後を読む lineReader。
func newLineReader(path string, offset int64) *lineReader {
	return &lineReader{path: path, size: offset, read: offset, lineStart: offset, line: make([]byte, 0, lineHeadLimit), chunk: make([]byte, readChunk)}
}

// unchanged は file の大きさ size が、前に読み終えたときから変わっていないか。
func (r *lineReader) unchanged(size int64) bool { return size == r.size }

// readTo は file の size までのうち、まだ読んでいない byte を読み、完結した行ごとに each を呼ぶ。head は行の頭 (改行を含めて
// lineHeadLimit まで)、start は行の頭の offset、length は改行を含めた行の長さ。file が前より縮んだら (truncate・置き換え)、
// 頭から読む。読めなければ何も進めずに error を返す (呼び出し側は、それまでに呼んだ each の結果を捨てる)。書きかけの行は次の
// 呼び出しで頭から読み直す。
func (r *lineReader) readTo(size int64, each func(head []byte, start, length int64)) error {
	before := r.lineStart
	if size < r.size {
		r.read, r.lineStart, r.line = 0, 0, r.line[:0]
	}
	if err := r.readLines(size, each); err != nil {
		r.read, r.lineStart, r.line = before, before, r.line[:0]
		return err
	}
	r.size = size
	return nil
}

func (r *lineReader) readLines(size int64, each func(head []byte, start, length int64)) error {
	f, err := os.Open(r.path)
	if err != nil {
		return err
	}
	defer f.Close()
	for r.read < size {
		n, err := f.ReadAt(r.chunk[:min(int64(len(r.chunk)), size-r.read)], r.read)
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
		r.line = append(r.line, part[:min(len(part), cap(r.line)-len(r.line))]...)
		r.read += int64(len(part))
		p = p[len(part):]
		if part[len(part)-1] == '\n' {
			each(r.line, r.lineStart, r.read-r.lineStart)
			r.lineStart, r.line = r.read, r.line[:0]
		}
	}
}
