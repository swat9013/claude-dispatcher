package worker

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// ResultSummary は attempt の最後の stream-json の result の行の要約 (formats.md §4)。その項目が無いか、型が違って読めなければ
// nil。
type ResultSummary struct {
	IsError               *bool
	NumTurns              *int
	PermissionDenialCount *int
}

// resultItems は stream-json の result の行のうち、要約に使う項目。型の違う項目があっても他の項目を読めるよう、項目は 1 つずつ
// decode する。
type resultItems struct {
	Type              string          `json:"type"`
	IsError           json.RawMessage `json:"is_error"`
	NumTurns          json.RawMessage `json:"num_turns"`
	PermissionDenials json.RawMessage `json:"permission_denials"`
}

// summarizeLastResult は file の offset より後の完結した行 (今の attempt が追記した行) から、最後の type: result の行を要約する。
// result の行が無ければ nil。最後まで読めなければ、どれが最後の result か決まらないので nil と理由を返す。項目の型が違えば、
// その項目を除いた要約とともに理由を返す。
func summarizeLastResult(file string, offset int64) (*ResultSummary, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(f)
	var last *resultItems
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			// 改行で終わらない末尾は書きかけの行
			break
		}
		if err != nil {
			return nil, err
		}
		// worker log は数 MB になり、ほとんどの行は result でないので、"result" を含む行だけを decode する
		if !bytes.Contains(line, []byte(`"result"`)) {
			continue
		}
		var items resultItems
		if json.Unmarshal(line, &items) != nil || items.Type != "result" {
			continue
		}
		last = &items
	}
	if last == nil {
		return nil, nil
	}
	return last.summarize()
}

// summarize は result の行の項目を要約する。型の違う項目は要約に載せず、その理由を返す。
func (items resultItems) summarize() (*ResultSummary, error) {
	var problems []string
	summary := &ResultSummary{
		IsError:  decodeItem[bool](items.IsError, "is_error", &problems),
		NumTurns: decodeItem[int](items.NumTurns, "num_turns", &problems),
	}
	if denials := decodeItem[[]json.RawMessage](items.PermissionDenials, "permission_denials", &problems); denials != nil {
		count := len(*denials)
		summary.PermissionDenialCount = &count
	}
	if len(problems) > 0 {
		return summary, fmt.Errorf("result の項目を読めない: %s", strings.Join(problems, "; "))
	}
	return summary, nil
}

// decodeItem は result の行の 1 項目を読む。項目が無いか null なら nil。型が違えば nil を返し、理由を problems に足す。
func decodeItem[T any](raw json.RawMessage, name string, problems *[]string) *T {
	if raw == nil {
		return nil
	}
	var value *T
	if err := json.Unmarshal(raw, &value); err != nil {
		*problems = append(*problems, fmt.Sprintf("%s: %v", name, err))
		return nil
	}
	return value
}
