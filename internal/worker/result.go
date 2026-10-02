package worker

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
)

// StreamResult は attempt の最後の stream-json の result の行の要約 (formats.md §4)。result の行にその項目が無ければ nil。
type StreamResult struct {
	IsError               *bool
	NumTurns              *int
	PermissionDenialCount *int
}

// resultLine は stream-json の 1 行のうち、result の要約に使う項目。
type resultLine struct {
	Type              string             `json:"type"`
	IsError           *bool              `json:"is_error"`
	NumTurns          *int               `json:"num_turns"`
	PermissionDenials *[]json.RawMessage `json:"permission_denials"`
}

// lastResult は file の offset より後の完結した行 (今の attempt が追記した行) から、最後の type: result の行を要約する。
// result の行が無ければ nil。
func lastResult(file string, offset int64) (*StreamResult, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(f)
	var last *StreamResult
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			// 改行で終わらない末尾は書きかけの行
			return last, nil
		}
		if err != nil {
			return nil, err
		}
		var l resultLine
		if json.Unmarshal(line, &l) != nil || l.Type != "result" {
			continue
		}
		last = &StreamResult{IsError: l.IsError, NumTurns: l.NumTurns}
		if l.PermissionDenials != nil {
			count := len(*l.PermissionDenials)
			last.PermissionDenialCount = &count
		}
	}
}
