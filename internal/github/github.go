// Package github は gh CLI を撃って tracker と CL host を読む。書き込みは持たない (system.md §5)。
package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/config"
	"github.com/swat9013/claude-dispatcher/internal/proc"
)

// Runner は gh を 1 回撃ち、stdout を返す。失敗は *Error。
type Runner interface {
	Run(args ...string) ([]byte, error)
}

// Error は gh の失敗 (観測不能)。Auth は認証が通らない失敗、NotFound は repo が見えない失敗で、
// どちらも他の失敗 (起動できない・timeout・network) と取り違えないために分ける (system.md §8)。
type Error struct {
	Args     []string
	Exit     int
	Stderr   string
	Auth     bool
	NotFound bool
}

func (e *Error) Error() string {
	head := strings.Join(e.Args[:min(2, len(e.Args))], " ")
	return fmt.Sprintf("gh %s failed (exit %d): %s", head, e.Exit, strings.TrimSpace(e.Stderr))
}

// IsAuth は err が gh の認証の失敗かを返す。
func IsAuth(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Auth
}

// IsNotFound は err が「repo が見えない」(綴りの誤りか、権限が無い) という gh の答えかを返す。
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.NotFound
}

// 認証が要るときの gh の exit code と、未認証 / token 失効のときに stderr へ出る文言 (system.md §8)
const authExit = 4

var authMarkers = []string{"HTTP 401", "gh auth login"}

// repo が見えないときに stderr へ出る文言 (GraphQL 経由の `repo view` と REST 経由の呼び出し)
var notFoundMarkers = []string{"Could not resolve to a Repository", "HTTP 404"}

// Exec は gh の実物を撃つ Runner。Path は解決済みの絶対 path、Env は子プロセスの env。
type Exec struct {
	Path    string
	Env     []string
	Timeout time.Duration
}

func (g Exec) Run(args ...string) ([]byte, error) {
	cmd := exec.Command(g.Path, args...)
	cmd.Env = g.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// process group の外へ逃げた孫が pipe を握ったままでも Wait が戻るよう、pipe の読み切りを待つ上限を置く
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return nil, &Error{Args: args, Exit: -1, Stderr: err.Error()}
	}
	timedOut, err := proc.Wait(cmd, g.Timeout)
	if timedOut {
		return nil, &Error{Args: args, Exit: -1, Stderr: fmt.Sprintf("%s を超えても終わらない", g.Timeout)}
	}
	if err != nil {
		text := stderr.String()
		return nil, &Error{
			Args: args, Exit: cmd.ProcessState.ExitCode(), Stderr: text,
			Auth:     cmd.ProcessState.ExitCode() == authExit || containsAny(text, authMarkers),
			NotFound: containsAny(text, notFoundMarkers),
		}
	}
	return stdout.Bytes(), nil
}

func containsAny(text string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// RepoExists は repo が見えるかを確かめる。
func RepoExists(gh Runner, repo config.Repo) error {
	_, err := gh.Run("repo", "view", repo.String(), "--json", "nameWithOwner")
	return err
}

// labelSearchLimit は label の検索 1 往復で読む件数の上限
const labelSearchLimit = 100

// LabelExists は repo に name の label があるかを返す。name で検索して綴りの一致を見るので、repo の label の総数に上限を持たない。
func LabelExists(gh Runner, repo config.Repo, name string) (bool, error) {
	out, err := gh.Run("label", "list", "-R", repo.String(), "--search", name, "--json", "name", "--limit", fmt.Sprint(labelSearchLimit))
	if err != nil {
		return false, err
	}
	var labels []struct{ Name string }
	if err := json.Unmarshal(out, &labels); err != nil {
		return false, fmt.Errorf("gh label list の出力を読めない: %w", err)
	}
	for _, l := range labels {
		if l.Name == name {
			return true, nil
		}
	}
	if len(labels) >= labelSearchLimit {
		// 検索は名前と説明の部分一致なので、一致する label が上限の外に居るかもしれない
		return false, fmt.Errorf("%w: label %q の検索結果が %d 件以上ある (%s)", ErrTruncated, name, labelSearchLimit, repo)
	}
	return false, nil
}

// ErrTruncated は 1 往復の上限に達し、観測が全量でないこと。切り詰めた像から指示を出さない
// (窓の外の open CL を持つ issue が候補へ戻り、二重着手になる)。
var ErrTruncated = errors.New("取得上限に達し観測が切り詰められた")

// IssueListLimit は 1 往復で読む open issue の上限
const IssueListLimit = 500

type Issue struct {
	Number int
	Title  string
	URL    string
	Body   string
	Labels []string
}

// OpenIssues は repo の open issue を全件返す。
func OpenIssues(gh Runner, repo config.Repo) ([]Issue, error) {
	out, err := gh.Run("issue", "list", "-R", repo.String(), "--state", "open",
		"--limit", fmt.Sprint(IssueListLimit), "--json", "number,title,labels,url,body")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Number int
		Title  string
		URL    string
		Body   string
		Labels []struct{ Name string }
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("gh issue list の出力を読めない: %w", err)
	}
	if len(raw) >= IssueListLimit {
		return nil, fmt.Errorf("%w: open issue が %d 件以上ある (%s)", ErrTruncated, IssueListLimit, repo)
	}
	issues := make([]Issue, 0, len(raw))
	for _, r := range raw {
		labels := make([]string, 0, len(r.Labels))
		for _, l := range r.Labels {
			labels = append(labels, l.Name)
		}
		issues = append(issues, Issue{Number: r.Number, Title: r.Title, URL: r.URL, Body: r.Body, Labels: labels})
	}
	return issues, nil
}

const (
	prPageSize          = 100
	closingRefsPageSize = 20
	reviewThreadsSize   = 100
)

// prQuery は open PR の紐づき (closing reference) と状態 (conflict / checks / 未解決 thread) を 1 往復で引く
var prQuery = fmt.Sprintf(`
query($owner: String!, $name: String!) {
  repository(owner: $owner, name: $name) {
    pullRequests(states: OPEN, first: %d, orderBy: {field: UPDATED_AT, direction: DESC}) {
      pageInfo { hasNextPage }
      nodes {
        number
        url
        headRefName
        baseRefName
        isDraft
        mergeable
        closingIssuesReferences(first: %d) {
          totalCount
          nodes { number repository { nameWithOwner } }
        }
        reviewThreads(first: %d) { totalCount nodes { isResolved } }
        commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
      }
    }
  }
}`, prPageSize, closingRefsPageSize, reviewThreadsSize)

// PR は open PR 1 本の観測。
type PR struct {
	Number    int
	URL       string
	Head      string
	Base      string
	Draft     bool
	Mergeable string
	// Closes は closing reference が指す issue (issueRepo の issue だけ)
	Closes            []int
	Checks            *string // head commit の checks の集約。checks が無ければ nil
	UnresolvedThreads int
}

// OpenPRs は repo の open PR を全件返す。closing reference は issueRepo を指すものだけを数える。
func OpenPRs(gh Runner, repo, issueRepo config.Repo) ([]PR, error) {
	// -f は生文字列。-F だと数字だけの owner / name が Int に型付けされ String! 変数に入らない
	out, err := gh.Run("api", "graphql", "-f", "query="+prQuery, "-f", "owner="+repo.Owner, "-f", "name="+repo.Name)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data struct {
			Repository struct {
				PullRequests struct {
					PageInfo struct{ HasNextPage bool }
					Nodes    []struct {
						Number                  int
						URL                     string
						HeadRefName             string
						BaseRefName             string
						IsDraft                 bool
						Mergeable               string
						ClosingIssuesReferences struct {
							TotalCount int
							Nodes      []struct {
								Number     int
								Repository struct{ NameWithOwner string }
							}
						}
						ReviewThreads struct {
							TotalCount int
							Nodes      []struct{ IsResolved bool }
						}
						Commits struct {
							Nodes []struct {
								Commit struct {
									StatusCheckRollup *struct{ State string }
								}
							}
						}
					}
				}
			}
		}
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, fmt.Errorf("gh api graphql の出力を読めない: %w", err)
	}
	page := payload.Data.Repository.PullRequests
	if page.PageInfo.HasNextPage {
		return nil, fmt.Errorf("%w: open PR が %d 件を超えた (%s)", ErrTruncated, prPageSize, repo)
	}
	prs := make([]PR, 0, len(page.Nodes))
	for _, n := range page.Nodes {
		if n.ClosingIssuesReferences.TotalCount > closingRefsPageSize {
			return nil, fmt.Errorf("%w: PR #%d の closing reference が %d 件を超えた", ErrTruncated, n.Number, closingRefsPageSize)
		}
		if n.ReviewThreads.TotalCount > reviewThreadsSize {
			return nil, fmt.Errorf("%w: PR #%d の review thread が %d 件を超えた", ErrTruncated, n.Number, reviewThreadsSize)
		}
		pr := PR{Number: n.Number, URL: n.URL, Head: n.HeadRefName, Base: n.BaseRefName, Draft: n.IsDraft, Mergeable: n.Mergeable, Closes: []int{}}
		for _, ref := range n.ClosingIssuesReferences.Nodes {
			if ref.Repository.NameWithOwner == issueRepo.String() {
				pr.Closes = append(pr.Closes, ref.Number)
			}
		}
		for _, t := range n.ReviewThreads.Nodes {
			if !t.IsResolved {
				pr.UnresolvedThreads++
			}
		}
		if len(n.Commits.Nodes) > 0 && n.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
			state := n.Commits.Nodes[0].Commit.StatusCheckRollup.State
			pr.Checks = &state
		}
		prs = append(prs, pr)
	}
	return prs, nil
}
