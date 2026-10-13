package worker

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
)

// progressLineLimit は、改行を含めてこれを超える行を type を見ずに progress と数える大きさ。1 行は頭のこの大きさまでだけを
// 持って判定する (formats.md §6 の progress)。活動の末尾の窓 (tailLimit) とは別に決める
const progressLineLimit = 64 << 10

// isProgress は stream-json の 1 行 (progressLineLimit 以下) が progress か (formats.md §6 の progress の表)。止まっていても
// 書かれる行 (heartbeat・rate limit の通知) と、中身を判定できない行だけを数えない。
func isProgress(line []byte) bool {
	var l struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &l); err != nil {
		return false
	}
	switch l.Type {
	case "", "tool_progress", "rate_limit_event":
		return false
	}
	return true
}

// scanProgress は file の from から to までのうち、完結した行 (改行で終わる行) をすべて見て、progress が 1 行でもあれば
// found を true で返す。end は見た最後の行の終わりの offset (改行の次)。完結していない行は次の確認で見直す。
func scanProgress(file string, from, to int64) (found bool, end int64, err error) {
	f, err := os.Open(file)
	if err != nil {
		return false, from, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(io.NewSectionReader(f, from, to-from), progressLineLimit)
	end = from
	head := make([]byte, 0, progressLineLimit)
	var length int64
	for {
		chunk, err := r.ReadSlice('\n')
		length += int64(len(chunk))
		head = append(head, chunk[:min(len(chunk), cap(head)-len(head))]...)
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return found, end, nil
		case err != nil:
			return false, from, err
		}
		end += length
		found = found || length > progressLineLimit || isProgress(head)
		head, length = head[:0], 0
	}
}
