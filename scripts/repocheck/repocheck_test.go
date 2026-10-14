package main

import (
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

var adrNames = []string{"0001-first.md", "0002-second.md", "0003-third.md"}

// validFiles は、すべての検査が一致する file の組を返す。各テストは変える file だけを上書きする
func validFiles() repoFiles {
	return repoFiles{ci: ciYAML, checks: checksYAML, release: releaseYAML, contributing: contributing, goMod: goMod, adrNames: adrNames}
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
	files.release = releaseYAML + "      - uses: goreleaser/goreleaser-action@abc # v7\n"

	if _, err := checkFiles(files); err == nil {
		t.Fatal("エラーにならなかった")
	}
}

func TestCheckFiles_一致していれば問題を返さない(t *testing.T) {
	problems, err := checkFiles(validFiles())

	if err != nil || len(problems) != 0 {
		t.Fatalf("got %q, %v", problems, err)
	}
}

func TestCheckFiles_GoReleaserの版が2箇所で違えば落とす(t *testing.T) {
	files := validFiles()
	files.release = strings.Replace(releaseYAML, "v2.18.2", "v2.19.0", 1)

	problems, _ := checkFiles(files)

	assertProblem(t, problems, "v2.19.0")
}

func TestCheckFiles_同じfileの2つ目のGoReleaserのstepの版が違っても落とす(t *testing.T) {
	files := validFiles()
	files.release = releaseYAML + "      - uses: goreleaser/goreleaser-action@abc # v7\n        with:\n          version: v2.19.0\n"

	problems, _ := checkFiles(files)

	assertProblem(t, problems, "v2.19.0")
}

func TestCheckFiles_CONTRIBUTINGの列挙にcheck名が欠けていれば落とす(t *testing.T) {
	files := validFiles()
	files.contributing = strings.Replace(contributing, "・`checks / lint`", "", 1)

	problems, _ := checkFiles(files)

	assertProblem(t, problems, "checks / lint")
}

func TestCheckFiles_CONTRIBUTINGの列挙に無いcheck名があれば落とす(t *testing.T) {
	files := validFiles()
	files.contributing = strings.Replace(contributing, "`checks / lint`", "`checks / lint`・`checks / gone`", 1)

	problems, _ := checkFiles(files)

	assertProblem(t, problems, "checks / gone")
}

func TestCheckFiles_go行が期待値とpatchだけ違っても落とす(t *testing.T) {
	files := validFiles()
	files.goMod = strings.Replace(goMod, "go 1.26.0", "go 1.26.5", 1)

	problems, _ := checkFiles(files)

	assertProblem(t, problems, "go.mod の `go` 行 (`1.26.5`) が CONTRIBUTING.md の期待値 (`1.26.0`) と違う")
}

func TestCheckFiles_go行がpatchの無い形に上がってもずれとして落とす(t *testing.T) {
	files := validFiles()
	files.goMod = strings.Replace(goMod, "go 1.26.0", "go 1.27", 1)

	problems, _ := checkFiles(files)

	assertProblem(t, problems, "go.mod の `go` 行 (`1.27`) が CONTRIBUTING.md の期待値 (`1.26.0`) と違う")
}

func TestCheckFiles_go行の後ろのコメントは版に含めない(t *testing.T) {
	files := validFiles()
	files.goMod = strings.Replace(goMod, "go 1.26.0", "go 1.26.0 // 最低の版", 1)

	problems, err := checkFiles(files)

	if err != nil || len(problems) != 0 {
		t.Fatalf("got %q, %v", problems, err)
	}
}

func TestCheckFiles_CONTRIBUTINGにgo行の期待値が無ければエラーにする(t *testing.T) {
	files := validFiles()
	files.contributing = strings.Replace(contributing, "期待値: `1.26.0`", "", 1)

	if _, err := checkFiles(files); err == nil {
		t.Fatal("エラーにならなかった")
	}
}

func TestCheckFiles_CONTRIBUTINGにgo行の期待値の目印が2回あればエラーにする(t *testing.T) {
	files := validFiles()
	files.contributing = "- 別の項目。期待値: `9.9.9`\n" + contributing

	if _, err := checkFiles(files); err == nil {
		t.Fatal("エラーにならなかった")
	}
}

func TestCheckFiles_go行の期待値が版の形でなければエラーにする(t *testing.T) {
	files := validFiles()
	files.contributing = strings.Replace(contributing, "期待値: `1.26.0`", "期待値: `1.26`", 1)

	if _, err := checkFiles(files); err == nil {
		t.Fatal("エラーにならなかった")
	}
}

func TestCheckFiles_go_modにgo行が無ければエラーにする(t *testing.T) {
	files := validFiles()
	files.goMod = strings.Replace(goMod, "go 1.26.0\n", "", 1)

	if _, err := checkFiles(files); err == nil {
		t.Fatal("エラーにならなかった")
	}
}

func TestCheckFiles_ADRの先頭4桁が重なれば番号とfile名を出して落とす(t *testing.T) {
	files := validFiles()
	files.adrNames = append(slices.Clone(adrNames), "0002-another.md")

	problems, _ := checkFiles(files)

	assertProblem(t, problems, "0002 が重なる: 0002-second.md / 0002-another.md")
}

func TestCheckFiles_ADRの先頭4桁が重ならなければ通す(t *testing.T) {
	files := validFiles()
	files.adrNames = append(slices.Clone(adrNames), "0004-fourth.md", "README.md")

	problems, err := checkFiles(files)

	if err != nil || len(problems) != 0 {
		t.Fatalf("got %q, %v", problems, err)
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

func assertProblem(t *testing.T, problems []string, want string) {
	t.Helper()
	for _, p := range problems {
		if strings.Contains(p, want) {
			return
		}
	}
	t.Fatalf("%q を含む問題が無い: %q", want, problems)
}
