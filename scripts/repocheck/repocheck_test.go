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

const contributing = "- PR に付く check 名: `checks / test (ubuntu-latest)`・`checks / test (macos-latest)`・`checks / lint`・`checks / goreleaser-check`。名前は `checks.yml` から決まる\n"

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
	}
	for name, checks := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := checkNames([]byte(ciYAML), []byte(checks)); err == nil {
				t.Fatal("エラーにならなかった")
			}
		})
	}
}

func TestCheckFiles_一致していれば問題を返さない(t *testing.T) {
	problems, err := checkFiles(repoFiles{ci: ciYAML, checks: checksYAML, release: releaseYAML, contributing: contributing})

	if err != nil || len(problems) != 0 {
		t.Fatalf("got %q, %v", problems, err)
	}
}

func TestCheckFiles_GoReleaserの版が2箇所で違えば落とす(t *testing.T) {
	release := strings.Replace(releaseYAML, "v2.18.2", "v2.19.0", 1)

	problems, _ := checkFiles(repoFiles{ci: ciYAML, checks: checksYAML, release: release, contributing: contributing})

	assertProblem(t, problems, "v2.19.0")
}

func TestCheckFiles_CONTRIBUTINGの列挙にcheck名が欠けていれば落とす(t *testing.T) {
	listed := strings.Replace(contributing, "・`checks / lint`", "", 1)

	problems, _ := checkFiles(repoFiles{ci: ciYAML, checks: checksYAML, release: releaseYAML, contributing: listed})

	assertProblem(t, problems, "checks / lint")
}

func TestCheckFiles_CONTRIBUTINGの列挙に無いcheck名があれば落とす(t *testing.T) {
	listed := strings.Replace(contributing, "`checks / lint`", "`checks / lint`・`checks / gone`", 1)

	problems, _ := checkFiles(repoFiles{ci: ciYAML, checks: checksYAML, release: releaseYAML, contributing: listed})

	assertProblem(t, problems, "checks / gone")
}

func TestCheckRuleset_required_checksが一致していれば問題を返さない(t *testing.T) {
	rules := `[{"type":"pull_request"},{"type":"required_status_checks","parameters":{"required_status_checks":[
		{"context":"checks / test (ubuntu-latest)"},{"context":"checks / test (macos-latest)"},{"context":"checks / lint"},{"context":"checks / goreleaser-check"}]}}]`

	problems, err := checkRuleset(wantNames, []byte(rules))

	if err != nil || len(problems) != 0 {
		t.Fatalf("got %q, %v", problems, err)
	}
}

func TestCheckRuleset_required_checksの過不足を落とす(t *testing.T) {
	rules := `[{"type":"required_status_checks","parameters":{"required_status_checks":[
		{"context":"checks / test (ubuntu-latest)"},{"context":"checks / test (macos-latest)"},{"context":"checks / goreleaser-check"},{"context":"checks / gone"}]}}]`

	problems, _ := checkRuleset(wantNames, []byte(rules))

	assertProblem(t, problems, "checks / lint")
	assertProblem(t, problems, "checks / gone")
}

func TestCheckRuleset_required_checksの規則が無ければエラーにする(t *testing.T) {
	if _, err := checkRuleset(wantNames, []byte(`[{"type":"pull_request"}]`)); err == nil {
		t.Fatal("エラーにならなかった")
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
