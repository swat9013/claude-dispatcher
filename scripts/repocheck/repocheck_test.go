package main

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const ciYAML = `
jobs:
  checks:
    uses: ./.github/workflows/checks.yml
`

const checksYAML = `
jobs:
  test:
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - run: go vet -tags blackbox ./...
  lint:
    runs-on: ubuntu-latest
  goreleaser-check:
    runs-on: ubuntu-latest
    steps:
      - uses: goreleaser/goreleaser-action@abc # v7
        with:
          version: v2.18.2
`

const releaseYAML = `
jobs:
  release:
    steps:
      - uses: actions/checkout@abc
      - uses: goreleaser/goreleaser-action@abc # v7
        with:
          version: v2.18.2
`

const contributing = "- PR に付く check 名: `checks / test (ubuntu-latest)`・`checks / test (macos-latest)`・`checks / lint`・`checks / goreleaser-check`。名前は `checks.yml` から決まる\n" +
	"- 手で見るもの: go.mod の `go` 行。期待値: `1.26.0`。`go` 行が期待値とずれると落ちる\n"

const goMod = "module example.com/m\n\ngo 1.26.0\n\ntoolchain go1.27.2\n"

const preCommitYAML = `
repos:
  - repo: local
    hooks:
      - id: go-vet
        entry: go vet -tags blackbox ./...
`

const golangciYAML = "version: \"2\"\nrun:\n  build-tags: [blackbox]\n"

var adrNames = []string{"0001-first.md", "0002-second.md", "0003-third.md"}

// validConstraints は、否定する build 制約の無い //go:build 行の組を返す
func validConstraints() map[string]string {
	return map[string]string{"cmd/x/slow_blackbox.go": "//go:build blackbox", "internal/proc/exit_other.go": "//go:build !linux && !darwin"}
}

// validFiles は、すべての検査が一致する file の組を返す。各テストは変える file だけを上書きする
func validFiles() repoFiles {
	return repoFiles{ci: ciYAML, checks: checksYAML, release: releaseYAML, contributing: contributing, goMod: goMod,
		preCommit: preCommitYAML, golangci: golangciYAML}
}

var wantNames = []string{"checks / test (ubuntu-latest)", "checks / test (macos-latest)", "checks / lint", "checks / goreleaser-check"}

func TestCheckNames_呼び出し元のjob_idとjob_idとmatrixの値から名前を組み立てる(t *testing.T) {
	got, err := checkNames([]byte(ciYAML), []byte(checksYAML))

	if err != nil || !slices.Equal(got, wantNames) {
		t.Fatalf("got %q, %v; want %q", got, err, wantNames)
	}
}

func TestCheckNames_組み立て方を知らない形はエラーにする(t *testing.T) {
	cases := map[string]string{
		"job の name":       "jobs:\n  lint:\n    name: Lint\n",
		"matrix の include": "jobs:\n  test:\n    strategy:\n      matrix:\n        include: [{os: a}]\n",
		"matrix の 2 次元":    "jobs:\n  test:\n    strategy:\n      matrix:\n        os: [a]\n        go: [b]\n",
		"job の if":         "jobs:\n  lint:\n    if: github.event_name == 'push'\n",
	}
	for name, checks := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := checkNames([]byte(ciYAML), []byte(checks)); err == nil {
				t.Fatal("エラーにならなかった")
			}
		})
	}
}

func TestCheckNames_呼び出し元のjobのifはエラーにする(t *testing.T) {
	ci := "jobs:\n  checks:\n    if: github.event_name == 'push'\n    uses: ./.github/workflows/checks.yml\n"

	if _, err := checkNames([]byte(ci), []byte(checksYAML)); err == nil {
		t.Fatal("エラーにならなかった")
	}
}

func TestCheckFiles_versionを渡さないGoReleaserのstepはエラーにする(t *testing.T) {
	files := validFiles()
	constraints := validConstraints()
	files.release = releaseYAML + "      - uses: goreleaser/goreleaser-action@abc # v7\n"

	if _, err := checkFiles(files, constraints); err == nil {
		t.Fatal("エラーにならなかった")
	}
}

func TestCheckFiles_一致していれば問題を返さない(t *testing.T) {
	problems, err := checkFiles(validFiles(), validConstraints())

	if err != nil || len(problems) != 0 {
		t.Fatalf("got %q, %v", problems, err)
	}
}

func TestCheckFiles_GoReleaserの版が2箇所で違えば落とす(t *testing.T) {
	files := validFiles()
	constraints := validConstraints()
	files.release = strings.Replace(releaseYAML, "v2.18.2", "v2.19.0", 1)

	problems, _ := checkFiles(files, constraints)

	assertProblem(t, problems, "v2.19.0")
}

func TestCheckFiles_同じfileの2つ目のGoReleaserのstepの版が違っても落とす(t *testing.T) {
	files := validFiles()
	constraints := validConstraints()
	files.release = releaseYAML + "      - uses: goreleaser/goreleaser-action@abc # v7\n        with:\n          version: v2.19.0\n"

	problems, _ := checkFiles(files, constraints)

	assertProblem(t, problems, "v2.19.0")
}

func TestCheckFiles_CONTRIBUTINGの列挙にcheck名が欠けていれば落とす(t *testing.T) {
	files := validFiles()
	constraints := validConstraints()
	files.contributing = strings.Replace(contributing, "・`checks / lint`", "", 1)

	problems, _ := checkFiles(files, constraints)

	assertProblem(t, problems, "checks / lint")
}

func TestCheckFiles_CONTRIBUTINGの列挙に無いcheck名があれば落とす(t *testing.T) {
	files := validFiles()
	constraints := validConstraints()
	files.contributing = strings.Replace(contributing, "`checks / lint`", "`checks / lint`・`checks / gone`", 1)

	problems, _ := checkFiles(files, constraints)

	assertProblem(t, problems, "checks / gone")
}

func TestCheckFiles_go行が期待値とpatchだけ違っても落とす(t *testing.T) {
	files := validFiles()
	constraints := validConstraints()
	files.goMod = strings.Replace(goMod, "go 1.26.0", "go 1.26.5", 1)

	problems, _ := checkFiles(files, constraints)

	assertProblem(t, problems, "go.mod の `go` 行 (`1.26.5`) が CONTRIBUTING.md の期待値 (`1.26.0`) と違う")
}

func TestCheckFiles_go行がpatchの無い形に上がってもずれとして落とす(t *testing.T) {
	files := validFiles()
	constraints := validConstraints()
	files.goMod = strings.Replace(goMod, "go 1.26.0", "go 1.27", 1)

	problems, _ := checkFiles(files, constraints)

	assertProblem(t, problems, "go.mod の `go` 行 (`1.27`) が CONTRIBUTING.md の期待値 (`1.26.0`) と違う")
}

func TestCheckFiles_go行の後ろのコメントは版に含めない(t *testing.T) {
	files := validFiles()
	constraints := validConstraints()
	files.goMod = strings.Replace(goMod, "go 1.26.0", "go 1.26.0 // 最低の版", 1)

	problems, err := checkFiles(files, constraints)

	if err != nil || len(problems) != 0 {
		t.Fatalf("got %q, %v", problems, err)
	}
}

func TestCheckFiles_CONTRIBUTINGにgo行の期待値が無ければエラーにする(t *testing.T) {
	files := validFiles()
	constraints := validConstraints()
	files.contributing = strings.Replace(contributing, "期待値: `1.26.0`", "", 1)

	if _, err := checkFiles(files, constraints); err == nil {
		t.Fatal("エラーにならなかった")
	}
}

func TestCheckFiles_CONTRIBUTINGにgo行の期待値の目印が2回あればエラーにする(t *testing.T) {
	files := validFiles()
	constraints := validConstraints()
	files.contributing = "- 別の項目。期待値: `9.9.9`\n" + contributing

	if _, err := checkFiles(files, constraints); err == nil {
		t.Fatal("エラーにならなかった")
	}
}

func TestCheckFiles_go行の期待値が版の形でなければエラーにする(t *testing.T) {
	files := validFiles()
	constraints := validConstraints()
	files.contributing = strings.Replace(contributing, "期待値: `1.26.0`", "期待値: `1.26`", 1)

	if _, err := checkFiles(files, constraints); err == nil {
		t.Fatal("エラーにならなかった")
	}
}

func TestCheckFiles_go_modにgo行が無ければエラーにする(t *testing.T) {
	files := validFiles()
	constraints := validConstraints()
	files.goMod = strings.Replace(goMod, "go 1.26.0\n", "", 1)

	if _, err := checkFiles(files, constraints); err == nil {
		t.Fatal("エラーにならなかった")
	}
}

func TestDuplicateADRNumbers_ADRの先頭4桁が重なれば番号とfile名を出して落とす(t *testing.T) {
	names := append(slices.Clone(adrNames), "0002-another.md")

	problems := duplicateADRNumbers(names)

	assertProblem(t, problems, "0002 が重なる: 0002-second.md / 0002-another.md")
}

func TestDuplicateADRNumbers_ADRの先頭4桁が重ならなければ通す(t *testing.T) {
	names := append(slices.Clone(adrNames), "0004-fourth.md")

	problems := duplicateADRNumbers(names)

	if len(problems) != 0 {
		t.Fatalf("got %q", problems)
	}
}

func TestDuplicateADRNumbers_区切りが違ってもADRの先頭4桁が重なれば落とす(t *testing.T) {
	names := append(slices.Clone(adrNames), "0003_other.md")

	problems := duplicateADRNumbers(names)

	assertProblem(t, problems, "0003 が重なる: 0003-third.md / 0003_other.md")
}

func TestDuplicateADRNumbers_先頭が番号でないmdとmdでないfileはADRの番号として数えない(t *testing.T) {
	names := append(slices.Clone(adrNames), "README.md", "0001-first.md.orig")

	problems := duplicateADRNumbers(names)

	if len(problems) != 0 {
		t.Fatalf("got %q", problems)
	}
}

func TestCheckADRNames_先頭がちょうど4桁でないか拡張子が小文字のmdでないADRの名前を1件1行で出す(t *testing.T) {
	for _, name := range []string{"17-other.md", "0017_other.MD", "017-other.md", "00170-other.md", "0017.md", "._0017-x.md"} {
		t.Run(name, func(t *testing.T) {
			problems := checkADRNames([]string{"docs/adr/0001-first.md", "docs/adr/" + name})

			if len(problems) != 1 {
				t.Fatalf("got %q", problems)
			}
			assertProblem(t, problems, name)
		})
	}
}

func TestCheckADRNames_READMEとmdで終わらないfileと区切りが違うADRは形の崩れとして出さない(t *testing.T) {
	paths := []string{"docs/adr/0001-first.md", "docs/adr/README.md", "docs/adr/0001-first.md.orig", "docs/adr/0004_other.md"}

	problems := checkADRNames(paths)

	if len(problems) != 0 {
		t.Fatalf("got %q", problems)
	}
}

func TestCheckRuleset_required_checksが一致していれば問題を返さない(t *testing.T) {
	rules := `[{"type":"pull_request"},{"type":"required_status_checks","parameters":{"required_status_checks":[
		{"context":"checks / test (ubuntu-latest)"},{"context":"checks / test (macos-latest)"},{"context":"checks / lint"},{"context":"checks / goreleaser-check"}]}}]`

	problems, err := checkRuleset(wantNames, []byte(rules))

	if err != nil || len(problems) != 0 {
		t.Fatalf("got %q, %v", problems, err)
	}
}

func TestCheckRuleset_required_checksにcheck名が欠けていれば落とす(t *testing.T) {
	rules := `[{"type":"required_status_checks","parameters":{"required_status_checks":[
		{"context":"checks / test (ubuntu-latest)"},{"context":"checks / test (macos-latest)"},{"context":"checks / goreleaser-check"}]}}]`

	problems, _ := checkRuleset(wantNames, []byte(rules))

	assertProblem(t, problems, "checks / lint")
}

func TestCheckRuleset_required_checksにcheck名に無い名前があれば落とす(t *testing.T) {
	rules := `[{"type":"required_status_checks","parameters":{"required_status_checks":[
		{"context":"checks / test (ubuntu-latest)"},{"context":"checks / test (macos-latest)"},{"context":"checks / lint"},{"context":"checks / goreleaser-check"},{"context":"checks / gone"}]}}]`

	problems, _ := checkRuleset(wantNames, []byte(rules))

	assertProblem(t, problems, "checks / gone")
}

func TestCheckRuleset_required_checksの規則が無ければずれとして落とす(t *testing.T) {
	problems, err := checkRuleset(wantNames, []byte(`[{"type":"pull_request"}]`))

	if err != nil {
		t.Fatal(err)
	}
	assertProblem(t, problems, "required_status_checks")
}

func TestCheckFiles_3箇所のbuild_tagの一覧がずれれば落とす(t *testing.T) {
	cases := map[string]func(*repoFiles){
		"pre-commit の go-vet": func(f *repoFiles) { f.preCommit = strings.Replace(preCommitYAML, " -tags blackbox", "", 1) },
		"checks.yml の go vet": func(f *repoFiles) { f.checks = strings.Replace(f.checks, "-tags blackbox", "-tags blackbox,other", 1) },
		"pre-commit の args": func(f *repoFiles) {
			f.preCommit = strings.Replace(preCommitYAML, "go vet -tags blackbox ./...", "go vet\n        args: [-tags, other, ./...]", 1)
		},
		".golangci.yml": func(f *repoFiles) { f.golangci = "run:\n  build-tags: []\n" },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			files := validFiles()
			constraints := validConstraints()
			edit(&files)

			problems, err := checkFiles(files, constraints)

			if err != nil {
				t.Fatal(err)
			}
			assertProblem(t, problems, "build tag の一覧が違う")
		})
	}
}

func TestCheckFiles_tagの順と重複と空の要素が違っても同じ一覧なら通す(t *testing.T) {
	files := validFiles()
	files.preCommit = strings.Replace(preCommitYAML, "-tags blackbox", "-tags other,blackbox,", 1)
	files.checks = strings.Replace(files.checks, "-tags blackbox", "-tags blackbox,other,blackbox", 1)
	files.golangci = "run:\n  build-tags: [other, blackbox]\n"

	problems, err := checkFiles(files, validConstraints())

	if err != nil || len(problems) != 0 {
		t.Fatalf("got %q, %v", problems, err)
	}
}

func TestCheckFiles_読まない書きかたのtagsはエラーにする(t *testing.T) {
	for _, entry := range []string{"go vet -tags=blackbox ./...", "go vet -tags blackbox --tags other ./...", "go vet -tags blackbox -tags other ./...", "go vet -tags blackbox\n        args: [-tags, other, ./...]"} {
		t.Run(entry, func(t *testing.T) {
			files := validFiles()
			files.preCommit = strings.Replace(preCommitYAML, "go vet -tags blackbox ./...", entry, 1)

			if _, err := checkFiles(files, validConstraints()); err == nil {
				t.Fatal("エラーにならなかった")
			}
		})
	}
}

func TestCheckFiles_go_vetのhookかstepが無ければエラーにする(t *testing.T) {
	cases := map[string]func(*repoFiles){
		"hook": func(f *repoFiles) { f.preCommit = "repos: []\n" },
		"step": func(f *repoFiles) { f.checks = strings.Replace(f.checks, "go vet", "go test", 1) },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			files := validFiles()
			constraints := validConstraints()
			edit(&files)

			if _, err := checkFiles(files, constraints); err == nil {
				t.Fatal("エラーにならなかった")
			}
		})
	}
}

func TestCheckFiles_検査するtagを否定するbuild制約のfileがあれば落とす(t *testing.T) {
	for _, line := range []string{"//go:build !blackbox", "//go:build linux && (darwin || !blackbox)", "//go:build !(blackbox || race)"} {
		t.Run(line, func(t *testing.T) {
			files := validFiles()
			constraints := validConstraints()
			constraints["cmd/x/slow_default.go"] = line

			problems, _ := checkFiles(files, constraints)

			assertProblem(t, problems, "cmd/x/slow_default.go の build 制約が tag blackbox を否定する")
		})
	}
}

func TestCheckFiles_二重に否定したtagは否定として数えない(t *testing.T) {
	files := validFiles()
	constraints := validConstraints()
	constraints["cmd/x/y.go"] = "//go:build !(!blackbox)"

	problems, err := checkFiles(files, constraints)

	if err != nil || len(problems) != 0 {
		t.Fatalf("got %q, %v", problems, err)
	}
}

func TestReadBuildConstraints_go_toolが読まないdirectoryとfileを読まず_package句より前の行だけを読む(t *testing.T) {
	root := t.TempDir()
	write := func(path, src string) {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	neg := "//go:build !blackbox\n\npackage x\n"
	write("cmd/a.go", "// c\n\n"+neg)
	write("cmd/after.go", "package x\n\n//go:build !blackbox\n")
	write(".claude/b.go", neg)
	write("_x/c.go", neg)
	write("testdata/d.go", neg)
	write("cmd/_e.go", neg)
	write("cmd/f.txt", neg)

	got, err := readBuildConstraints(root)

	want := map[string]string{filepath.Join(root, "cmd/a.go"): "//go:build !blackbox"}
	if err != nil || !maps.Equal(got, want) {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func assertProblem(t *testing.T, problems []string, want string) {
	t.Helper()
	for _, p := range problems {
		if strings.Contains(p, want) {
			return
		}
	}
	t.Fatalf("%q を含む問題が無い: %q", want, problems)
}
