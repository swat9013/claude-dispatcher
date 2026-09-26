package config

import (
	"fmt"
	"regexp"
	"strings"
)

// Repo は `owner/name` の置き場。config の読込で 1 度だけ分解し、以降は分解済みの形で持ち回る。
type Repo struct {
	Owner string
	Name  string
}

func (r Repo) String() string { return r.Owner + "/" + r.Name }

// ParseRepo は `owner/name` を分解する。形が違えば error。
func ParseRepo(s string) (Repo, error) {
	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Repo{}, fmt.Errorf("repo %q は owner/name の形でなければならない", s)
	}
	return Repo{Owner: parts[0], Name: parts[1]}, nil
}

var githubRemote = regexp.MustCompile(`github\.com[:/]([^/\s]+)/([^/\s]+?)(\.git)?/?$`)

// RepoFromRemote は git の remote URL (`git@github.com:o/n.git` / `https://github.com/o/n` 等) を置き場として読む。
// GitHub 以外の URL は読めない。
func RepoFromRemote(url string) (Repo, error) {
	m := githubRemote.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return Repo{}, fmt.Errorf("GitHub の remote URL として読めない: %q", strings.TrimSpace(url))
	}
	return Repo{Owner: m[1], Name: m[2]}, nil
}

// SameRepo は 2 つの置き場が同じか (GitHub の owner / name は大文字小文字を区別しない)。
func SameRepo(a, b Repo) bool { return strings.EqualFold(a.String(), b.String()) }

// Repos は実在を確かめる置き場の列 (issue 置き場と、別なら CL 置き場)。
func (c Config) Repos() []Repo {
	if c.CLRepo == c.IssueRepo {
		return []Repo{c.IssueRepo}
	}
	return []Repo{c.IssueRepo, c.CLRepo}
}
