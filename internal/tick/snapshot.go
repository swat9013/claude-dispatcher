package tick

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/config"
	"github.com/swat9013/claude-dispatcher/internal/github"
)

// Snapshot は 1 tick の観測の正規化像 (formats.md §5.1)。tick を跨いで持ち越さない。
type Snapshot struct {
	ObservedAt string         `json:"observed_at"`
	IssueRepo  string         `json:"issue_repo"`
	CLRepo     string         `json:"cl_repo"`
	Limits     Limits         `json:"limits"`
	Observed   ObservedCounts `json:"observed"`
	Issues     IssueBuckets   `json:"issues"`
	// LinkedCLs は open issue → 紐づく open CL の番号 (key は issue 番号の文字列)
	LinkedCLs map[string][]int `json:"linked_cls"`
	CLs       []CL             `json:"cls"`
}

type Limits struct {
	MaxWIP   int `json:"max_wip"`
	WIPCount int `json:"wip_count"`
}

type ObservedCounts struct {
	Issues int `json:"issues"`
	CLs    int `json:"cls"`
}

type IssueBuckets struct {
	Candidates    []Candidate  `json:"candidates"`
	WIP           []IssueBrief `json:"wip"`
	ReadyForHuman []int        `json:"ready_for_human"`
}

type IssueBrief struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

// Candidate は候補。候補だけが issue 本文を持つ (orchestrator の playbook 選定の信号)
type Candidate struct {
	IssueBrief
	Body string `json:"body"`
}

type CL struct {
	Number            int     `json:"number"`
	URL               string  `json:"url"`
	Branch            string  `json:"branch"`
	Base              string  `json:"base"`
	Draft             bool    `json:"draft"`
	Issues            []int   `json:"issues"`
	Mergeable         string  `json:"mergeable"`
	Checks            *string `json:"checks"`
	UnresolvedThreads int     `json:"unresolved_threads"`
}

// WorkerBranch は worker が作る branch 名の規約 (system.md §10)。CLI はこの綴りで worker 由来の CL を見分ける
func WorkerBranch(issue int) string { return fmt.Sprintf("worktree-issue-%d", issue) }

// Classify は open issue と open PR から snapshot を組む。
//
// issue に紐づく CL は、closing reference がその issue を指すものと、head branch が worker の規約
// `worktree-issue-<issue>` のものの和 (system.md §4)。
func Classify(cfg config.Config, issues []github.Issue, prs []github.PR, now time.Time) Snapshot {
	open := map[int]bool{}
	for _, i := range issues {
		open[i.Number] = true
	}
	cls := make([]CL, 0, len(prs))
	linked := map[int][]int{}
	for _, pr := range prs {
		related := slices.Clone(pr.Closes)
		for n := range open {
			if pr.Head == WorkerBranch(n) && !slices.Contains(related, n) {
				related = append(related, n)
			}
		}
		slices.Sort(related)
		for _, n := range related {
			if open[n] {
				linked[n] = append(linked[n], pr.Number)
			}
		}
		cls = append(cls, CL{
			Number: pr.Number, URL: pr.URL, Branch: pr.Head, Base: pr.Base, Draft: pr.Draft, Issues: related,
			Mergeable: pr.Mergeable, Checks: pr.Checks, UnresolvedThreads: pr.UnresolvedThreads,
		})
	}

	buckets := IssueBuckets{Candidates: []Candidate{}, WIP: []IssueBrief{}, ReadyForHuman: []int{}}
	for _, i := range issues {
		brief := IssueBrief{Number: i.Number, Title: i.Title, URL: i.URL}
		wip := slices.Contains(i.Labels, config.WIPLabel)
		human := slices.Contains(i.Labels, config.HumanLabel)
		if wip {
			buckets.WIP = append(buckets.WIP, brief)
		}
		if human {
			buckets.ReadyForHuman = append(buckets.ReadyForHuman, i.Number)
		}
		if slices.Contains(i.Labels, cfg.ReadyLabel) && !wip && !human && len(linked[i.Number]) == 0 {
			buckets.Candidates = append(buckets.Candidates, Candidate{IssueBrief: brief, Body: i.Body})
		}
	}

	linkedCLs := map[string][]int{}
	for n, numbers := range linked {
		linkedCLs[strconv.Itoa(n)] = numbers
	}
	return Snapshot{
		ObservedAt: now.UTC().Format(logTimeLayout),
		IssueRepo:  cfg.IssueRepo,
		CLRepo:     cfg.CLRepo,
		Limits:     Limits{MaxWIP: cfg.MaxWIP, WIPCount: len(buckets.WIP)},
		Observed:   ObservedCounts{Issues: len(issues), CLs: len(prs)},
		Issues:     buckets,
		LinkedCLs:  linkedCLs,
		CLs:        cls,
	}
}
