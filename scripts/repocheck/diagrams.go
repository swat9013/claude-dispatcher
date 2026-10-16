package main

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	designDir = "docs/design"
	// plantumlVersionPath は gate で描く plantuml の版。scripts/plantuml-check.sh と CI の導入の step も同じ file を読む
	plantumlVersionPath = "scripts/plantuml.version"
)

var (
	// plantuml は描いた svg に、描いた版と元の source (@startuml と @enduml の間) を埋める
	svgVersionForm = regexp.MustCompile(`<\?plantuml ([^?\s]+)\?>`)
	svgSourceForm  = regexp.MustCompile(`<\?plantuml-src ([0-9A-Za-z_-]+)\?>`)
)

// plantumlEncoding は plantuml が source を raw deflate した後に使う符号。bit の並びは base64 と同じで、文字の表だけが違う
var plantumlEncoding = base64.NewEncoding("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-_").WithPadding(base64.NoPadding)

// runDiagrams は docs/design の .puml と、隣の同名の .svg を読み、検査の結果を返す。svg が無ければ空文字で渡す
func runDiagrams() ([]string, error) {
	pinBytes, err := os.ReadFile(plantumlVersionPath)
	if err != nil {
		return nil, err
	}
	pin := strings.TrimSpace(string(pinBytes))
	pumls, err := filepath.Glob(filepath.Join(designDir, "*.puml"))
	if err != nil {
		return nil, err
	}
	if len(pumls) == 0 {
		return nil, fmt.Errorf("%s に .puml が無い", designDir)
	}
	var problems []string
	for _, p := range pumls {
		puml, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		svg, err := os.ReadFile(strings.TrimSuffix(p, ".puml") + ".svg")
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		problems = append(problems, checkDiagram(p, string(puml), string(svg), pin)...)
	}
	return problems, nil
}

// checkDiagram は、svg が pin の版の plantuml で今の puml から描かれたかを、svg に埋まった版と source で検査する
// (描き直して比べない理由は CONTRIBUTING.md の「gate」)
func checkDiagram(pumlPath, puml, svg, pin string) []string {
	svgPath := strings.TrimSuffix(pumlPath, ".puml") + ".svg"
	redraw := fmt.Sprintf("plantuml %s で `plantuml -tsvg %s` を撃って描き直す", pin, pumlPath)
	if n := strings.Count(puml, "@startuml"); n != 1 {
		return []string{fmt.Sprintf("%s: @startuml が %d 個ある。docs/design の .puml には図を 1 つだけ書く", pumlPath, n)}
	}
	if svg == "" {
		return []string{fmt.Sprintf("%s: 描いた svg (%s) が無い。%s", pumlPath, svgPath, redraw)}
	}
	v := svgVersionForm.FindStringSubmatch(svg)
	if v == nil {
		return []string{fmt.Sprintf("%s: plantuml の描いた版が見当たらない。%s", svgPath, redraw)}
	}
	if v[1] != pin {
		return []string{fmt.Sprintf("%s: plantuml %s で描かれている (固定した版は %s)。%s", svgPath, v[1], pin, redraw)}
	}
	s := svgSourceForm.FindStringSubmatch(svg)
	if s == nil {
		return []string{fmt.Sprintf("%s: 描いた元の source が埋まっていない。%s", svgPath, redraw)}
	}
	src, err := decodePlantumlSource(s[1])
	if err != nil {
		return []string{fmt.Sprintf("%s: 描いた元の source を読めない (%v)。%s", svgPath, err, redraw)}
	}
	if src != pumlBody(puml) {
		return []string{fmt.Sprintf("%s: %s を直した後に描き直していない。%s", svgPath, pumlPath, redraw)}
	}
	return nil
}

// pumlBody は plantuml が svg に埋める形 (@startuml と @enduml の行を除き、先頭の空行と末尾の改行を落とした本文) に揃える。
// 図が 1 つだけであることは checkDiagram が先に確かめる
func pumlBody(puml string) string {
	lines := strings.Split(strings.ReplaceAll(puml, "\r\n", "\n"), "\n")
	start, end := 0, len(lines)
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "@startuml") {
			start = i + 1
			break
		}
	}
	for i := len(lines) - 1; i >= start; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "@enduml") {
			end = i
			break
		}
	}
	// 落とすのは空の行だけ。空白だけの行は plantuml も残す
	for start < end && lines[start] == "" {
		start++
	}
	return strings.TrimRight(strings.Join(lines[start:end], "\n"), "\n")
}

func decodePlantumlSource(enc string) (string, error) {
	// 末尾の 4 文字に満たない端数は、余りの bit を 0 で埋めて符号にしている
	raw, err := plantumlEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	b, err := io.ReadAll(flate.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}
