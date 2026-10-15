package guide

import (
	"slices"
	"strings"
	"testing"
)

const sample = "# 題\n\n## 書き方\n\n```markdown\n# fenced block の中は見出しでない\n| key | 中身 |\n|---|---|\n| `x` | 表でない |\n```\n\n### 項目\n\n散文。\n\n| key | 型 |\n|---|---|\n| `a` | 文字列 |\n| `b` | 真偽 |\n\n| key | `jira` での意味 |\n|---|---|\n| `a` | 対応 |\n\n| 宣言 | GitHub |\n|---|---|\n| `a` | 書ける |\n\n### 空\n\n| key | GitLab の field |\n|---|---|\n| `a` | `x` |\n\n## 次\n\n本文\n"

func TestASectionEndsAtTheNextHeadingOfTheSameOrShallowerLevel(t *testing.T) {
	s, err := FindSection(sample, "書き方")

	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.Body, "### 項目") || strings.Contains(s.Body, "## 次") {
		t.Fatalf("節の範囲が違う:\n%s", s.Body)
	}
}

func TestAHashLineInAFencedBlockIsNotAHeading(t *testing.T) {
	_, err := FindSection(sample, "fenced block の中は見出しでない")

	if err == nil {
		t.Fatal("fenced block の中の行を見出しとみなした")
	}
}

func TestAMissingSectionIsAnErrorNamingIt(t *testing.T) {
	_, err := FindSection(sample, "無い節")

	if err == nil || !strings.Contains(err.Error(), "無い節") {
		t.Fatalf("節を名指しして落ちない: %v", err)
	}
}

func TestKeyTablesExcludeTablesNamingAServiceAndTablesWhoseFirstHeaderIsNotKey(t *testing.T) {
	s, _ := FindSection(sample, "項目")

	tables, err := s.KeyTables()

	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || !slices.Equal(tables[0].Header, []string{"key", "型"}) {
		t.Fatalf("key の表だけを返さない: %v", tables)
	}
	if got := [][]string{{"`a`", "文字列"}, {"`b`", "真偽"}}; !slices.EqualFunc(tables[0].Rows, got, slices.Equal) {
		t.Fatalf("行が違う: %v", tables[0].Rows)
	}
}

func TestASectionWithoutAKeyTableIsAnErrorNamingTheSection(t *testing.T) {
	s, _ := FindSection(sample, "空")

	_, err := s.KeyTables()

	if err == nil || !strings.Contains(err.Error(), "空") {
		t.Fatalf("節を名指しして落ちない: %v", err)
	}
}

func TestTablesInAFencedBlockAreNotTables(t *testing.T) {
	s, _ := FindSection(sample, "書き方")

	for _, table := range s.Tables() {
		for _, row := range table.Rows {
			if row[0] == "`x`" {
				t.Fatalf("fenced block の中の表を表とみなした: %v", table)
			}
		}
	}
}

func TestFencedBlocksReturnTheirInfoAndLines(t *testing.T) {
	s, _ := FindSection(sample, "書き方")

	blocks := s.FencedBlocks()

	if len(blocks) != 1 || blocks[0].Info != "markdown" || len(blocks[0].Lines) != 4 {
		t.Fatalf("fenced block が違う: %+v", blocks)
	}
}
