// Package github は gh CLI を撃って tracker と CL host を読む。書き込みは持たない (system.md §5)。
package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Runner は gh を 1 回撃ち、stdout を返す。失敗は *Error。
type Runner interface {
	Run(args ...string) ([]byte, error)
}

// Error は gh の失敗 (観測不能)。Auth は認証が通らない失敗で、config の綴りの失敗と取り違えないために分ける。
type Error struct {
	Args   []string
	Exit   int
	Stderr string
	Auth   bool
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

// 認証が要るときの gh の exit code と、未認証 / token 失効のときに stderr へ出る文言 (system.md §8)
const authExit = 4

var authMarkers = []string{"HTTP 401", "gh auth login"}

// Exec は gh の実物を撃つ Runner。Path は解決済みの絶対 path、Env は子プロセスの env。
type Exec struct {
	Path    string
	Env     []string
	Timeout time.Duration
}

func (g Exec) Run(args ...string) ([]byte, error) {
	cmd := exec.Command(g.Path, args...)
	cmd.Env = g.Env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return nil, &Error{Args: args, Exit: -1, Stderr: err.Error()}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			exit := cmd.ProcessState.ExitCode()
			text := stderr.String()
			auth := exit == authExit
			for _, marker := range authMarkers {
				auth = auth || strings.Contains(text, marker)
			}
			return nil, &Error{Args: args, Exit: exit, Stderr: text, Auth: auth}
		}
		return stdout.Bytes(), nil
	case <-time.After(g.Timeout):
		_ = cmd.Process.Kill()
		<-done
		return nil, &Error{Args: args, Exit: -1, Stderr: fmt.Sprintf("%s を超えても終わらない", g.Timeout)}
	}
}

// RepoExists は repo が見えるかを確かめる。
func RepoExists(gh Runner, repo string) error {
	_, err := gh.Run("repo", "view", repo, "--json", "nameWithOwner")
	return err
}

// LabelListLimit は 1 往復で読む label の上限。上限に達したら検査できないとして落とす
const LabelListLimit = 500

// Labels は repo の label 名を返す。
func Labels(gh Runner, repo string) ([]string, error) {
	out, err := gh.Run("label", "list", "-R", repo, "--json", "name", "--limit", fmt.Sprint(LabelListLimit))
	if err != nil {
		return nil, err
	}
	var labels []struct{ Name string }
	if err := json.Unmarshal(out, &labels); err != nil {
		return nil, fmt.Errorf("gh label list の出力を読めない: %w", err)
	}
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		names = append(names, l.Name)
	}
	return names, nil
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
func OpenIssues(gh Runner, repo string) ([]Issue, error) {
	out, err := gh.Run("issue", "list", "-R", repo, "--state", "open",
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
func OpenPRs(gh Runner, repo, issueRepo string) ([]PR, error) {
	owner, name, _ := strings.Cut(repo, "/")
	// -f は生文字列。-F だと数字だけの owner / name が Int に型付けされ String! 変数に入らない
	out, err := gh.Run("api", "graphql", "-f", "query="+prQuery, "-f", "owner="+owner, "-f", "name="+name)
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
			if ref.Repository.NameWithOwner == issueRepo {
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
