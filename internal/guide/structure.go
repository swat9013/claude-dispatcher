package guide

import (
	"fmt"
	"strings"
)

// 案内の構造 (節・表・fenced block) の読み手。案内と code の同期を確かめる test が、案内の散文の文面に依らず
// 構造から綴りを得るためにある。案内は人が書くので (ADR 0012)、読み手が頼るのは markdown の構造と表の見出し行だけにする。

// Section は案内の見出しの節。Body は見出しの次の行から、同じか浅い深さの次の見出しの前まで
// (深い見出しの節を含む)。fenced block の中の `#` の行は見出しとみなさない。
type Section struct {
	Heading string
	Body    string
}

// Table は markdown の表。Header は見出し行のセル、Rows は区切り行より後の行のセル。セルの前後の空白は除く。
type Table struct {
	Header []string
	Rows   [][]string
}

// FencedBlock は ``` で囲んだ block。Info は開きの ``` に続く綴り、Lines は中身の行。
type FencedBlock struct {
	Info  string
	Lines []string
}

// line は本文の 1 行と、それが fenced block の中 (開き・閉じの行を含む) かどうか。
type line struct {
	text   string
	fenced bool
}

func lines(body string) []line {
	var out []line
	inFence := false
	for _, text := range strings.Split(body, "\n") {
		isFence := strings.HasPrefix(strings.TrimSpace(text), "```")
		out = append(out, line{text: text, fenced: inFence || isFence})
		if isFence {
			inFence = !inFence
		}
	}
	return out
}

// heading は行が見出しなら、その深さと題を返す。
func heading(l line) (int, string, bool) {
	if l.fenced {
		return 0, "", false
	}
	level := len(l.text) - len(strings.TrimLeft(l.text, "#"))
	if level == 0 || !strings.HasPrefix(l.text[level:], " ") {
		return 0, "", false
	}
	return level, strings.TrimSpace(l.text[level:]), true
}

// FindSection は text の、題が heading の節。どの深さの見出しでもよい。無ければ error。
func FindSection(text, title string) (Section, error) {
	ls := lines(text)
	for i, l := range ls {
		level, got, ok := heading(l)
		if !ok || got != title {
			continue
		}
		end := len(ls)
		for j := i + 1; j < len(ls); j++ {
			if next, _, ok := heading(ls[j]); ok && next <= level {
				end = j
				break
			}
		}
		var body []string
		for _, b := range ls[i+1 : end] {
			body = append(body, b.text)
		}
		return Section{Heading: title, Body: strings.Join(body, "\n")}, nil
	}
	return Section{}, fmt.Errorf("案内に節 %q が無い", title)
}

func cells(text string) []string {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "|")
	text = strings.TrimSuffix(text, "|")
	parts := strings.Split(text, "|")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

func isTableLine(l line) bool {
	return !l.fenced && strings.HasPrefix(strings.TrimSpace(l.text), "|")
}

// Tables は節の本文の表を、出てくる順に返す。fenced block の中の行は表とみなさない。
func (s Section) Tables() []Table {
	var tables []Table
	ls := lines(s.Body)
	for i := 0; i < len(ls); i++ {
		if !isTableLine(ls[i]) {
			continue
		}
		start := i
		for i < len(ls) && isTableLine(ls[i]) {
			i++
		}
		block := ls[start:i]
		if len(block) < 2 {
			continue
		}
		t := Table{Header: cells(block[0].text)}
		for _, row := range block[2:] {
			t.Rows = append(t.Rows, cells(row.text))
		}
		tables = append(tables, t)
	}
	return tables
}

// FencedBlocks は節の本文の fenced block を、出てくる順に返す。
func (s Section) FencedBlocks() []FencedBlock {
	var blocks []FencedBlock
	var current *FencedBlock
	for _, l := range lines(s.Body) {
		switch {
		case !l.fenced:
		case current == nil:
			// 開きの行。fenced の判定は lines に 1 つだけ置く
			current = &FencedBlock{Info: strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l.text), "```"))}
		case strings.HasPrefix(strings.TrimSpace(l.text), "```"):
			blocks = append(blocks, *current)
			current = nil
		default:
			current.Lines = append(current.Lines, l.text)
		}
	}
	return blocks
}

// isKeyTable は、表が key の表であること。1 列目の見出しが `key` で、2 列目の見出しが services のどれも名指ししない
// (大文字と小文字を区別しない)。provider の対応表 (`jira` での意味・GitLab の field) は 2 列目の見出しでサービスを名指しするので外れる。
func (t Table) isKeyTable(services []string) bool {
	if len(t.Header) < 2 || t.Header[0] != "key" {
		return false
	}
	second := strings.ToLower(t.Header[1])
	for _, s := range services {
		if strings.Contains(second, strings.ToLower(s)) {
			return false
		}
	}
	return true
}

// KeyTables は節の key の表 (isKeyTable) を、出てくる順に返す。services は置き場のサービスの綴りで、種類の知識は provider が
// 持つので呼び出し側が渡す。1 つも無ければ、節を名指しした error。
func (s Section) KeyTables(services []string) ([]Table, error) {
	var out []Table
	for _, t := range s.Tables() {
		if t.isKeyTable(services) {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("案内の節 %q に key の表 (1 列目の見出しが key で、2 列目の見出しが置き場のサービスを名指ししない表) が無い", s.Heading)
	}
	return out, nil
}
