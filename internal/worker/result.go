package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// isResult は完結した行が type: result の行か。worker log は数 MB になり、ほとんどの行は result でないので、"result" を
// 含む行だけを decode する。
func isResult(line []byte) bool {
	if !bytes.Contains(line, []byte(`"result"`)) {
		return false
	}
	var items resultItems
	return json.Unmarshal(line, &items) == nil && items.Type == "result"
}

// summarizeResult は result の行を要約する。
func summarizeResult(line []byte) (*ResultSummary, error) {
	var items resultItems
	if err := json.Unmarshal(line, &items); err != nil {
		return nil, err
	}
	return items.summarize()
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
