package main

import (
	"strings"
	"testing"
)

func TestRemoteHostIsReadFromEachSpellingOfTheURL(t *testing.T) {
	for _, tc := range []struct {
		remote string
		want   string
	}{
		{"https://github.com/acme/widgets.git", "github.com"},
		{"https://user:token@GitLab.Example.com:8443/acme/sub/widgets.git", "gitlab.example.com"},
		{"ssh://git@gitlab.example.com:2222/acme/sub/widgets.git", "gitlab.example.com"},
		{"git@gitlab.example.com:acme/sub/widgets.git", "gitlab.example.com"},
		{"gitlab.example.com:acme/widgets.git", "gitlab.example.com"},
	} {
		t.Run(tc.remote, func(t *testing.T) {
			got, err := remoteHost(tc.remote)

			if err != nil || got != tc.want {
				t.Fatalf("remoteHost(%q) = %q, %v, want %q", tc.remote, got, err, tc.want)
			}
		})
	}
}

func TestRemoteHostDoesNotRepeatAnUnreadableURLThatMayCarryCredentials(t *testing.T) {
	_, err := remoteHost("https://user:s3cret@gitlab.example.com:bad/acme/widgets.git")

	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("err = %v, want 認証情報を含まない誤り", err)
	}
}

func TestRemoteHostOfALocalPathIsAnError(t *testing.T) {
	for _, remote := range []string{"/srv/git/widgets.git", "../widgets", "./a:b"} {
		t.Run(remote, func(t *testing.T) {
			if got, err := remoteHost(remote); err == nil {
				t.Fatalf("remoteHost(%q) = %q, want error", remote, got)
			}
		})
	}
}
