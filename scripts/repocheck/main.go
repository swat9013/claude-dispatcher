// repocheck は、同じ値を 2 箇所以上に書いている設定の一致を検査する (方針は CONTRIBUTING.md の「gate」)。
// network は使わない。ruleset は呼び出し側が gh で取った JSON を標準入力で渡す。
//
//	go run ./scripts/repocheck files     # repo の root で撃つ。pre-commit の hook が撃つ
//	gh api repos/<owner>/<repo>/rules/branches/main | go run ./scripts/repocheck ruleset
//
// 不一致は 1 件 1 行で標準エラーに出し、exit 1 で終わる。検査そのものの失敗 (file が読めない等) は exit 2
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ciPath           = ".github/workflows/ci.yml"
	checksPath       = ".github/workflows/checks.yml"
	releasePath      = ".github/workflows/release.yml"
	contributingPath = "CONTRIBUTING.md"

	goreleaserAction = "goreleaser/goreleaser-action@"
	// CONTRIBUTING.md の check 名の列挙がある行の目印。列挙はこの後から最初の「。」までの `...` で囲んだ名前
	checkNamesMarker = "PR に付く check 名:"
)

func main() {
	if len(os.Args) != 2 {
		fatal(errors.New("使い方: repocheck files | repocheck ruleset < rules.json"))
	}
	var problems []string
	var err error
	switch os.Args[1] {
	case "files":
		var files repoFiles
		files, err = readRepoFiles()
		if err == nil {
			problems, err = checkFiles(files)
		}
	case "ruleset":
		problems, err = runRuleset()
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
	ci, checks, release, contributing string
}

func readRepoFiles() (repoFiles, error) {
	read := func(path string) (string, error) {
		b, err := os.ReadFile(path)
		return string(b), err
	}
	var f repoFiles
	var errs [4]error
	f.ci, errs[0] = read(ciPath)
	f.checks, errs[1] = read(checksPath)
	f.release, errs[2] = read(releasePath)
	f.contributing, errs[3] = read(contributingPath)
	return f, errors.Join(errs[:]...)
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

// checkFiles は repo の file だけで決まる一致を検査する
func checkFiles(f repoFiles) ([]string, error) {
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
	return problems, nil
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
