package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// testdata/diagrams/sample.svg は sample.puml を plantuml 1.2026.8 の `-tsvg` で描いたもの
func readSample(t *testing.T) (puml, svg string) {
	t.Helper()
	p, err := os.ReadFile("testdata/diagrams/sample.puml")
	if err != nil {
		t.Fatal(err)
	}
	s, err := os.ReadFile("testdata/diagrams/sample.svg")
	if err != nil {
		t.Fatal(err)
	}
	return string(p), string(s)
}

func TestCheckDiagram_固定した版で描き直したsvgは通す(t *testing.T) {
	puml, svg := readSample(t)
	if got := checkDiagram("sample.puml", puml, svg, "1.2026.8"); len(got) != 0 {
		t.Fatalf("問題を返した: %v", got)
	}
}

// testdata/diagrams/blank.svg は、@startuml の直後に空の行 2 つと空白だけの行を置いた blank.puml を plantuml 1.2026.8 で描いたもの。
// plantuml は空の行を落とし、空白だけの行は残して埋める
func TestCheckDiagram_startumlの直後に空行を置いたpumlも描き直せば通す(t *testing.T) {
	puml, err := os.ReadFile("testdata/diagrams/blank.puml")
	if err != nil {
		t.Fatal(err)
	}
	svg, err := os.ReadFile("testdata/diagrams/blank.svg")
	if err != nil {
		t.Fatal(err)
	}
	if got := checkDiagram("blank.puml", string(puml), string(svg), "1.2026.8"); len(got) != 0 {
		t.Fatalf("問題を返した: %v", got)
	}
}

func TestCheckDiagram_pumlを直してsvgを描き直していなければ落とす(t *testing.T) {
	puml, svg := readSample(t)
	puml = strings.Replace(puml, "終わる", "終える", 1)
	got := checkDiagram("sample.puml", puml, svg, "1.2026.8")
	if len(got) != 1 || !strings.Contains(got[0], "描き直していない") {
		t.Fatalf("描き直し漏れとして落とさなかった: %v", got)
	}
}

func TestCheckDiagram_固定した版と違う版で描いたsvgは落とす(t *testing.T) {
	puml, svg := readSample(t)
	got := checkDiagram("sample.puml", puml, svg, "1.2026.9")
	if len(got) != 1 || !strings.Contains(got[0], "1.2026.9") {
		t.Fatalf("版のずれとして落とさなかった: %v", got)
	}
}

func TestCheckDiagram_描いた元のsourceを持たないsvgは落とす(t *testing.T) {
	puml, _ := readSample(t)
	got := checkDiagram("sample.puml", puml, `<svg><?plantuml 1.2026.8?></svg>`, "1.2026.8")
	if len(got) != 1 {
		t.Fatalf("source の無い svg を通した: %v", got)
	}
}

func TestCheckDiagram_図を2つ書いたpumlは落とす(t *testing.T) {
	puml, svg := readSample(t)
	got := checkDiagram("sample.puml", puml+"@startuml other\n[*] --> X\n@enduml\n", svg, "1.2026.8")
	if len(got) != 1 || !strings.Contains(got[0], "2 個") {
		t.Fatalf("図が 2 つの puml を落とさなかった: %v", got)
	}
}

func TestCheckDiagram_svgが無ければ落とす(t *testing.T) {
	puml, _ := readSample(t)
	if got := checkDiagram("sample.puml", puml, "", "1.2026.8"); len(got) != 1 {
		t.Fatalf("svg の無い puml を通した: %v", got)
	}
}

func TestDesignPumls_ドットで始まる名前は行き先の無いsymlinkでも検査の対象から外す(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "statechart.puml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nobody@host.1234", filepath.Join(dir, ".#statechart.puml")); err != nil {
		t.Fatal(err)
	}

	pumls, err := designPumls(dir)

	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(dir, "statechart.puml")}; !slices.Equal(pumls, want) {
		t.Fatalf("got %q, want %q", pumls, want)
	}
}
