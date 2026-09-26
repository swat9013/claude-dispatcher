package config

import (
	"fmt"
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
