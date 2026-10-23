// repocheck は、同じ値を 2 箇所以上に書いている設定の一致 (go vet と golangci-lint に渡す build tag の一覧と、その tag を否定する build 制約の file が無いことを含む) と、ADR の番号の一意と、ADR の file 名の形と、docs/design の図の描き直し漏れを検査する (方針は CONTRIBUTING.md の「gate」)。
// network は使わない。ruleset は呼び出し側が gh で取った JSON を標準入力で渡す。
//
//	go run ./scripts/repocheck files     # repo の root で撃つ。pre-commit の hook が撃つ
//	go run ./scripts/repocheck adr       # ADR の番号の一意。commit 時の hook と adr-numbers.yml が撃つ (PR の gate では撃たない)
//	go run ./scripts/repocheck adr-names docs/adr/<file>...  # 渡した ADR の file 名の形。commit 時の hook が撃ち、CI の pre-commit job でも撃つ
//	go run ./scripts/repocheck diagrams  # docs/design の svg が、固定した版の plantuml で今の .puml から描かれたか。commit 時の hook が撃つ
//	gh api repos/<owner>/<repo>/rules/branches/main | go run ./scripts/repocheck ruleset
//
// 不一致は 1 件 1 行で標準エラーに出し、exit 1 で終わる。検査そのものの失敗 (file が読めない等) は exit 2
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/build/constraint"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ciPath           = ".github/workflows/ci.yml"
	checksPath       = ".github/workflows/checks.yml"
	releasePath      = ".github/workflows/release.yml"
	contributingPath = "CONTRIBUTING.md"
	goModPath        = "go.mod"
	preCommitPath    = ".pre-commit-config.yaml"
	golangciPath     = ".golangci.yml"
	adrDir           = "docs/adr"

	goreleaserAction = "goreleaser/goreleaser-action@"
	// CONTRIBUTING.md の check 名の列挙がある行の目印。列挙はこの後から最初の「。」までの `...` で囲んだ名前
	checkNamesMarker = "PR に付く check 名:"
	// CONTRIBUTING.md の go.mod の `go` 行の期待値の目印。期待値はこの直後の `...` で囲んだ版。
	// 別の値を読まないよう、file に 1 回だけ現れることを求める
	goVersionMarker = "期待値: `"
)

// goVersionForm は期待値が取る版の形 (patch まで書いた版)
var goVersionForm = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// adrNameForm は ADR の file 名の形: 先頭がちょうど 4 桁の番号で、拡張子が小文字の .md。
// この形でない file は番号の検査から外し、ADR のつもりの名前 (isADRIntended) なら malformedADRNames が報告する
var adrNameForm = regexp.MustCompile(`^([0-9]{4})[^0-9].*\.md$`)

func main() {
	// adr-names だけが file 名を 1 つ以上取る。0 個で黙って通ると、手で再現したつもりで何も検査しない
	if len(os.Args) < 2 || (os.Args[1] == "adr-names") != (len(os.Args) > 2) {
		fatal(errors.New("使い方: repocheck files | repocheck adr | repocheck adr-names <file>... | repocheck diagrams | repocheck ruleset < rules.json"))
	}
	var problems []string
	var err error
	switch os.Args[1] {
	case "files":
		var files repoFiles
		files, err = readRepoFiles()
		if err == nil {
			var constraints map[string]string
			constraints, err = readBuildConstraints(".")
			if err == nil {
				problems, err = checkFiles(files, constraints)
			}
		}
	case "adr":
		var names []string
		names, err = readADRNames()
		if err == nil {
			problems = duplicateADRNumbers(names)
		}
	case "adr-names":
		problems = checkADRNames(os.Args[2:])
	case "ruleset":
		problems, err = runRuleset()
	case "diagrams":
		problems, err = runDiagrams()
	default:
		err = fmt.Errorf("知らない検査: %s", os.Args[1])
	}
	if err != nil {
		fatal(err)
	}
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, p)
	}
	if len(problems) > 0 {
		os.Exit(1)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "repocheck:", err)
	os.Exit(2)
}

type repoFiles struct {
	ci, checks, release, contributing, goMod, preCommit, golangci string
}

func readRepoFiles() (repoFiles, error) {
	read := func(path string) (string, error) {
		b, err := os.ReadFile(path)
		return string(b), err
	}
	var f repoFiles
	var errs [7]error
	f.ci, errs[0] = read(ciPath)
	f.checks, errs[1] = read(checksPath)
	f.release, errs[2] = read(releasePath)
	f.contributing, errs[3] = read(contributingPath)
	f.goMod, errs[4] = read(goModPath)
	f.preCommit, errs[5] = read(preCommitPath)
	f.golangci, errs[6] = read(golangciPath)
	return f, errors.Join(errs[:]...)
}

// readBuildConstraints は repo の .go の file の //go:build 行を読む。go の tool が読まない directory
// (`.`・`_` で始まる名前と testdata) には入らない。`.` で始まる名前を外すのは CONTRIBUTING.md の「gate」の方針で、
// .claude/worktrees の他の作業ツリーも読まずに済む
func readBuildConstraints(root string) (map[string]string, error) {
	constraints := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || !strings.HasSuffix(name, ".go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if line := goBuildLine(string(b)); line != "" {
			constraints[path] = line
		}
		return nil
	})
	return constraints, err
}

// goBuildLine は package 句より前にある //go:build 行を返す (無ければ空)
func goBuildLine(src string) string {
	for line := range strings.Lines(src) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			break
		}
		if constraint.IsGoBuild(line) {
			return line
		}
	}
	return ""
}

// readADRNames は docs/adr の file の名前を返す (directory は除く)。adr の検査だけが読む
func readADRNames() ([]string, error) {
	entries, err := os.ReadDir(adrDir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

func runRuleset() ([]string, error) {
	files, err := readRepoFiles()
	if err != nil {
		return nil, err
	}
	names, err := checkNames([]byte(files.ci), []byte(files.checks))
	if err != nil {
		return nil, err
	}
	rules, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil, err
	}
	return checkRuleset(names, rules)
}

// checkFiles は repo の file だけで決まる一致を検査する。constraints は .go の file の path から //go:build 行への対応 (readBuildConstraints)
func checkFiles(f repoFiles, constraints map[string]string) ([]string, error) {
	var problems []string

	checksVersions, err := goreleaserVersions([]byte(f.checks))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", checksPath, err)
	}
	releaseVersions, err := goreleaserVersions([]byte(f.release))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", releasePath, err)
	}
	versions := append(slices.Clone(checksVersions), releaseVersions...)
	if slices.ContainsFunc(versions, func(v string) bool { return v != versions[0] }) {
		problems = append(problems, fmt.Sprintf("GoReleaser の版が違う: %s は %s、%s は %s",
			checksPath, strings.Join(checksVersions, " / "), releasePath, strings.Join(releaseVersions, " / ")))
	}

	names, err := checkNames([]byte(f.ci), []byte(f.checks))
	if err != nil {
		return nil, err
	}
	listed, err := listedCheckNames(f.contributing)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", contributingPath, err)
	}
	problems = append(problems, setDiff(names, listed, fmt.Sprintf("%s の check 名の列挙", contributingPath))...)

	goVersion, err := goDirective(f.goMod)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", goModPath, err)
	}
	expected, err := expectedGoVersion(f.contributing)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", contributingPath, err)
	}
	if goVersion != expected {
		problems = append(problems, fmt.Sprintf("go.mod の `go` 行 (`%s`) が CONTRIBUTING.md の期待値 (`%s`) と違う。"+
			"利用者に求める版が上がってよいかを人が判断し、受け入れるなら CONTRIBUTING.md の「版の更新」の期待値を書き換える (worker は書き換えずに人へ返す)",
			goVersion, expected))
	}

	tagProblems, err := checkBuildTags(f, constraints)
	if err != nil {
		return nil, err
	}
	return append(problems, tagProblems...), nil
}

// checkBuildTags は、go vet と golangci-lint に渡す build tag の一覧が 3 箇所 (.pre-commit-config.yaml の go-vet の hook・
// checks.yml の go vet の step・.golangci.yml の run.build-tags) で一致するかと、その tag を否定する build 制約
// (`//go:build !blackbox` 等) の file が無いかを検査する。tag 付きでだけ検査するので、否定する file は配布する binary に入るのに
// go vet からも golangci-lint からも外れる
func checkBuildTags(f repoFiles, constraints map[string]string) ([]string, error) {
	preCommitTags, err := preCommitVetTags([]byte(f.preCommit))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", preCommitPath, err)
	}
	checksTags, err := workflowVetTags([]byte(f.checks))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", checksPath, err)
	}
	golangciTags, err := golangciBuildTags([]byte(f.golangci))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", golangciPath, err)
	}

	var problems []string
	lists := append([][]string{preCommitTags}, checksTags...)
	lists = append(lists, golangciTags)
	if slices.ContainsFunc(lists, func(l []string) bool { return !slices.Equal(l, golangciTags) }) {
		var checks []string
		for _, l := range checksTags {
			checks = append(checks, fmt.Sprintf("%q", l))
		}
		problems = append(problems, fmt.Sprintf("go vet と golangci-lint に渡す build tag の一覧が違う: %s の go-vet の hook は %q、%s の go vet の step は %s、%s の run.build-tags は %q",
			preCommitPath, preCommitTags, checksPath, strings.Join(checks, " / "), golangciPath, golangciTags))
	}

	tags := slices.Concat(lists...)
	for _, path := range slices.Sorted(maps.Keys(constraints)) {
		expr, err := constraint.Parse(constraints[path])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if tag, ok := negatedTag(expr, tags); ok {
			problems = append(problems, fmt.Sprintf("%s の build 制約が tag %s を否定する。go vet と golangci-lint は tag %s 付きでだけ検査するので、この file はどちらからも外れる。否定を使わない形にする",
				path, tag, tag))
		}
	}
	return problems, nil
}

// negatedTag は、expr の中で否定 (奇数回の !) の下に現れる tag のうち tags に入るものを返す。
// `!(blackbox || race)` のように括弧の式ごと否定した形も拾う
func negatedTag(expr constraint.Expr, tags []string) (string, bool) {
	return negatedTagUnder(expr, tags, false)
}

// negatedTagUnder は negatedTag の再帰。negated はここまでに通った ! の偶奇
func negatedTagUnder(expr constraint.Expr, tags []string, negated bool) (string, bool) {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		return e.Tag, negated && slices.Contains(tags, e.Tag)
	case *constraint.NotExpr:
		return negatedTagUnder(e.X, tags, !negated)
	case *constraint.AndExpr:
		if t, ok := negatedTagUnder(e.X, tags, negated); ok {
			return t, ok
		}
		return negatedTagUnder(e.Y, tags, negated)
	case *constraint.OrExpr:
		if t, ok := negatedTagUnder(e.X, tags, negated); ok {
			return t, ok
		}
		return negatedTagUnder(e.Y, tags, negated)
	}
	return "", false
}

// golangciBuildTags は .golangci.yml の run.build-tags を並べ替えて重複を除いて返す
func golangciBuildTags(src []byte) ([]string, error) {
	var config struct {
		Run struct {
			BuildTags []string `yaml:"build-tags"`
		} `yaml:"run"`
	}
	if err := yaml.Unmarshal(src, &config); err != nil {
		return nil, err
	}
	return slices.Compact(slices.Sorted(slices.Values(config.Run.BuildTags))), nil
}

// preCommitVetTags は .pre-commit-config.yaml の go-vet の hook の entry と args に渡す -tags の一覧を返す。hook がちょうど 1 つでなければエラーにする
func preCommitVetTags(src []byte) ([]string, error) {
	var config struct {
		Repos []struct {
			Hooks []struct {
				ID    string   `yaml:"id"`
				Entry string   `yaml:"entry"`
				Args  []string `yaml:"args"`
			} `yaml:"hooks"`
		} `yaml:"repos"`
	}
	if err := yaml.Unmarshal(src, &config); err != nil {
		return nil, err
	}
	var entries []string
	for _, r := range config.Repos {
		for _, h := range r.Hooks {
			if h.ID == "go-vet" {
				// pre-commit は entry の後ろに args を足して撃つので、args の -tags も効く
				entries = append(entries, strings.Join(append([]string{h.Entry}, h.Args...), " "))
			}
		}
	}
	if len(entries) != 1 {
		return nil, fmt.Errorf("go-vet の hook が %d 個ある (1 個を求める)", len(entries))
	}
	return vetTags(entries[0])
}

// workflowVetTags は workflow の run: のうち go vet を撃つ行ごとに、-tags の一覧を返す。go vet の行が無ければエラーにする
func workflowVetTags(src []byte) ([][]string, error) {
	_, js, err := jobs(src)
	if err != nil {
		return nil, err
	}
	var lists [][]string
	for _, j := range js {
		for _, s := range j.Steps {
			for line := range strings.Lines(s.Run) {
				if f := strings.Fields(line); len(f) >= 2 && f[0] == "go" && f[1] == "vet" {
					tags, err := vetTags(line)
					if err != nil {
						return nil, err
					}
					lists = append(lists, tags)
				}
			}
		}
	}
	if len(lists) == 0 {
		return nil, errors.New("go vet を撃つ step が無い")
	}
	return lists, nil
}

// vetTags は go vet のコマンド行の `-tags x,y` の一覧を並べ替えて重複を除いて返す。-tags が無ければ空。
// 他の書き方 (`-tags=x`・`--tags` 等) は、読み違えて通らないようエラーにする。-tags が 2 回あってもエラーにする
func vetTags(cmd string) ([]string, error) {
	fields := strings.Fields(cmd)
	var values []string
	for i, f := range fields {
		if f != "-tags" && (strings.HasPrefix(f, "-tags=") || strings.HasPrefix(f, "--tags")) {
			return nil, fmt.Errorf("%q の %s は読まない書き方 (`-tags x,y` の形にする)", cmd, f)
		}
		if f == "-tags" {
			if i+1 == len(fields) {
				return nil, fmt.Errorf("%q の -tags に値が無い", cmd)
			}
			values = append(values, fields[i+1])
		}
	}
	if len(values) > 1 {
		return nil, fmt.Errorf("%q に -tags が 2 回ある", cmd)
	}
	var tags []string
	if len(values) == 1 {
		// go は空の要素を捨てるので、ここでも捨てる
		tags = slices.DeleteFunc(strings.Split(values[0], ","), func(t string) bool { return t == "" })
	}
	return slices.Compact(slices.Sorted(slices.Values(tags))), nil
}

// duplicateADRNumbers は、先頭の番号が重なる ADR の file 名を、番号ごとに 1 件 1 行で返す。
// ADR を足す branch がそれぞれ次の空き番号を手で取るので、並行すると merge の後に番号が重なる。
// merge の後の重なりは CL の変更と無関係に PR の merge ref を赤くするので、files と分けて PR の gate から外す
func duplicateADRNumbers(names []string) []string {
	byNumber := map[string][]string{}
	for _, name := range names {
		if m := adrNameForm.FindStringSubmatch(name); m != nil {
			byNumber[m[1]] = append(byNumber[m[1]], name)
		}
	}
	var problems []string
	for _, number := range slices.Sorted(maps.Keys(byNumber)) {
		if dup := byNumber[number]; len(dup) > 1 {
			problems = append(problems, fmt.Sprintf("%s の ADR の番号 %s が重なる: %s。後から足した方を次の空き番号へ振り直す",
				adrDir, number, strings.Join(dup, " / ")))
		}
	}
	return problems
}

// checkADRNames は repocheck adr-names が報告する問題 (渡した file 名の形の崩れ) を返す。
// 作業ツリーの directory を読まず、hook が渡す git に載った file だけを見る (git に載っていない lock file 等で落ちないように)
func checkADRNames(paths []string) []string {
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = filepath.Base(p)
	}
	return malformedADRNames(names)
}

// malformedADRNames は、ADR のつもりの名前のうち adrNameForm に合わないものを 1 件 1 行で返す。
// 合わない名前は番号の検査から黙って外れるので、重なりも命名規則 (先頭 4 桁) の崩れも見逃される
func malformedADRNames(names []string) []string {
	var problems []string
	for _, name := range names {
		if isADRIntended(name) && !adrNameForm.MatchString(name) {
			problems = append(problems, fmt.Sprintf("%s の ADR の file 名 %s が形に合わない: 先頭をちょうど 4 桁の番号にし、その直後に数字でない文字 (区切り) と名前を置き、拡張子を小文字の .md にする (0017.md のような番号だけの名前は形に合わない)",
				adrDir, name))
		}
	}
	return problems
}

// isADRIntended は ADR のつもりの名前か: 大文字小文字を問わず .md で終わり、README.md でない。
// .md で終わらない file (merge で残った .orig 等) は数えない。数えると commit 時の hook が CL と無関係に落ちる
func isADRIntended(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".md") && name != "README.md"
}

// checkRuleset は ruleset の required status checks が check 名と一致するかを検査する。
// rules は GET /repos/{owner}/{repo}/rules/branches/{branch} の応答
func checkRuleset(names []string, rules []byte) ([]string, error) {
	var parsed []struct {
		Type       string `json:"type"`
		Parameters struct {
			RequiredStatusChecks []struct {
				Context string `json:"context"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(rules, &parsed); err != nil {
		return nil, fmt.Errorf("ruleset の JSON を読めない: %w", err)
	}
	var required []string
	found := false
	for _, r := range parsed {
		if r.Type != "required_status_checks" {
			continue
		}
		found = true
		for _, c := range r.Parameters.RequiredStatusChecks {
			required = append(required, c.Context)
		}
	}
	// 規則が無いのは、gate が外れた最も大きいずれなので、検査の失敗でなくずれとして返す
	if !found {
		return []string{"ruleset に required_status_checks の規則が無い (PR の CI が通らなくても merge できる)"}, nil
	}
	return setDiff(names, required, "ruleset の required status checks"), nil
}

// setDiff は、check 名 (want) と where に書かれた名前 (got) の過不足を 1 件 1 行で返す
func setDiff(want, got []string, where string) []string {
	var problems []string
	for _, n := range want {
		if !slices.Contains(got, n) {
			problems = append(problems, fmt.Sprintf("%s に %q が無い (%s の job から決まる check 名)", where, n, checksPath))
		}
	}
	for _, n := range got {
		if !slices.Contains(want, n) {
			problems = append(problems, fmt.Sprintf("%s の %q は %s の job から決まる check 名に無い", where, n, checksPath))
		}
	}
	return problems
}

type workflow struct {
	Jobs yaml.Node `yaml:"jobs"`
}

type job struct {
	Name     string `yaml:"name"`
	If       string `yaml:"if"`
	Uses     string `yaml:"uses"`
	Strategy struct {
		Matrix yaml.Node `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []struct {
		Uses string            `yaml:"uses"`
		Run  string            `yaml:"run"`
		With map[string]string `yaml:"with"`
	} `yaml:"steps"`
}

// jobs は workflow の job を書いた順に返す (check 名の列挙の順を file の順に揃えるため map にしない)
func jobs(src []byte) ([]string, []job, error) {
	var w workflow
	if err := yaml.Unmarshal(src, &w); err != nil {
		return nil, nil, err
	}
	if w.Jobs.Kind != yaml.MappingNode {
		return nil, nil, errors.New("jobs が無い")
	}
	var ids []string
	var js []job
	for i := 0; i < len(w.Jobs.Content); i += 2 {
		var j job
		if err := w.Jobs.Content[i+1].Decode(&j); err != nil {
			return nil, nil, err
		}
		ids = append(ids, w.Jobs.Content[i].Value)
		js = append(js, j)
	}
	return ids, js, nil
}

// checkNames は、ci.yml が checks.yml を呼ぶ job の id と、checks.yml の job id・matrix の値から、
// PR に付く check 名 (`<呼び出し元の job id> / <job id>[ (<matrix の値>)]`) を組み立てる。
// 組み立て方を確かめていない形 (job の name:・job の if:・matrix の include / exclude・2 次元以上の matrix) はエラーにする
func checkNames(ci, checks []byte) ([]string, error) {
	ciIDs, ciJobs, err := jobs(ci)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ciPath, err)
	}
	caller := ""
	for i, j := range ciJobs {
		if j.Uses == "./"+checksPath {
			if err := unverifiedJobForm(j); err != nil {
				return nil, fmt.Errorf("%s: job %s: %w", ciPath, ciIDs[i], err)
			}
			caller = ciIDs[i]
		}
	}
	if caller == "" {
		return nil, fmt.Errorf("%s: %s を呼ぶ job が無い", ciPath, checksPath)
	}

	ids, js, err := jobs(checks)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", checksPath, err)
	}
	var names []string
	for i, j := range js {
		if err := unverifiedJobForm(j); err != nil {
			return nil, fmt.Errorf("%s: job %s: %w", checksPath, ids[i], err)
		}
		prefix := caller + " / " + ids[i]
		m := j.Strategy.Matrix
		if m.Kind == 0 {
			names = append(names, prefix)
			continue
		}
		var values []string
		if m.Kind != yaml.MappingNode || len(m.Content) != 2 || m.Content[0].Value == "include" || m.Content[0].Value == "exclude" ||
			m.Content[1].Decode(&values) != nil {
			return nil, fmt.Errorf("%s: job %s の matrix は 1 次元の値の列しか扱わない", checksPath, ids[i])
		}
		for _, v := range values {
			names = append(names, fmt.Sprintf("%s (%s)", prefix, v))
		}
	}
	return names, nil
}

// unverifiedJobForm は、check 名の組み立て方を確かめていない job の書き方をエラーで返す。
// name: は check 名を変え、if: は PR で check が付かないことがある
func unverifiedJobForm(j job) error {
	switch {
	case j.Name != "":
		return errors.New("name: から check 名を組み立てる方法を確かめていない")
	case j.If != "":
		return errors.New("if: で PR に check が付くかを確かめていない")
	}
	return nil
}

// goreleaserVersions は workflow で goreleaser-action に渡す version: を、step の順にすべて返す
func goreleaserVersions(src []byte) ([]string, error) {
	_, js, err := jobs(src)
	if err != nil {
		return nil, err
	}
	var versions []string
	for _, j := range js {
		for _, s := range j.Steps {
			if strings.HasPrefix(s.Uses, goreleaserAction) {
				if s.With["version"] == "" {
					return nil, errors.New("goreleaser-action に version: を渡していない step がある")
				}
				versions = append(versions, s.With["version"])
			}
		}
	}
	if len(versions) == 0 {
		return nil, errors.New("goreleaser-action の step が無い")
	}
	return versions, nil
}

// listedCheckNames は CONTRIBUTING.md の check 名の列挙を返す。列挙は「PR に付く check 名:」の後から最初の「。」までの、
// バッククォートで囲んだ語。目印が無ければエラーにする
func listedCheckNames(src string) ([]string, error) {
	_, after, ok := strings.Cut(src, checkNamesMarker)
	if !ok {
		return nil, fmt.Errorf("%q の行が無い", checkNamesMarker)
	}
	list, _, _ := strings.Cut(after, "。")
	var names []string
	for i, part := range strings.Split(list, "`") {
		if i%2 == 1 {
			names = append(names, part)
		}
	}
	return names, nil
}

// goDirective は go.mod の `go` 行の版を、形を問わずそのまま返す。`go 1.27` のような patch の無い引き上げも、
// ずれとして期待値と比べるため。`go` 行は block に入らない 1 行の directive で、後ろに // のコメントが付きうる
func goDirective(src string) (string, error) {
	for line := range strings.Lines(src) {
		line, _, _ = strings.Cut(line, "//")
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "go" {
			continue
		}
		if len(fields) != 2 {
			return "", fmt.Errorf("`go` 行の版を読めない: %q", strings.TrimSpace(line))
		}
		return fields[1], nil
	}
	return "", errors.New("`go` 行が無い")
}

// expectedGoVersion は CONTRIBUTING.md の go.mod の `go` 行の期待値を返す。期待値は「期待値: 」の直後の、
// バッククォートで囲んだ版。目印が無いか 2 回以上現れるか、版の形でなければエラーにする
func expectedGoVersion(src string) (string, error) {
	parts := strings.Split(src, goVersionMarker)
	switch {
	case len(parts) == 1:
		return "", fmt.Errorf("%q の行が無い", goVersionMarker)
	case len(parts) > 2:
		return "", fmt.Errorf("%q が %d 回ある (go.mod の `go` 行の期待値だけに 1 回書く)", goVersionMarker, len(parts)-1)
	}
	version, _, ok := strings.Cut(parts[1], "`")
	if !ok || !goVersionForm.MatchString(version) {
		return "", fmt.Errorf("%q の後の版を読めない", goVersionMarker)
	}
	return version, nil
}
