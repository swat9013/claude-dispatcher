package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/github"
	"github.com/swat9013/claude-dispatcher/internal/gitlab"
	"github.com/swat9013/claude-dispatcher/internal/proc"
	"github.com/swat9013/claude-dispatcher/internal/scaffold"
)

// runSetup は `setup [<workflow の path>]` を撃つ (formats.md §7.4): cwd の clone の origin から tracker.kind と issue 置き場を
// 決め、その種類の雛形を書く。既にある file は上書きしない。
func runSetup(args []string, stdout, stderr io.Writer) int {
	path, ok := workflowArg(args, nil, stderr)
	if !ok {
		return exitUsage
	}
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(stdout, "%s は既にあるので書かない\n", path)
		return 0
	}
	template, place, err := scaffoldFromOrigin()
	if err != nil {
		fmt.Fprintf(stderr, "issue 置き場を決められない: %v\n", err)
		return exitFailed
	}
	// 確かめてから書くまでの間に他が書いても、上書きしない
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, os.ErrExist) {
		fmt.Fprintf(stdout, "%s は既にあるので書かない\n", path)
		return 0
	}
	if err == nil {
		_, err = io.WriteString(file, template)
		err = errors.Join(err, file.Close())
	}
	if err != nil {
		fmt.Fprintf(stderr, "workflow 定義の雛形を書けない (%s): %v\n", path, err)
		return exitFailed
	}
	fmt.Fprintf(stdout, "%s に workflow 定義の雛形を書いた (issue 置き場: %s)。project に合わせて直し、`claude-dispatcher loop --dry-run` で試運転する\n", path, place)
	return 0
}

// scaffoldFromOrigin は cwd の clone の origin の host で tracker.kind を決め、issue 置き場を埋めたその種類の雛形と、issue 置き場の
// 表示名を返す。host が github.com なら github、それ以外は gitlab。
// PATH は撃つ CLI ごとに解決する (git の後、host で決まった tracker の CLI だけ)。
func scaffoldFromOrigin() (template, place string, err error) {
	origin, err := newEnvironment("git").origin()
	if err != nil {
		return "", "", err
	}
	host, err := remoteHost(origin)
	if err != nil {
		// URL は認証情報を含みうるので名指しに載せない
		return "", "", fmt.Errorf("origin の URL: %w", err)
	}
	if host == "github.com" {
		repo, err := github.CurrentRepo(newEnvironment("gh").gh(""))
		if err != nil {
			return "", "", err
		}
		return scaffold.GitHub(repo.String()), repo.String(), nil
	}
	// path を host と同じ remote から決めるので、glab にも origin の URL を渡す (glab 自身の remote の選び方に依らない)
	project, err := gitlab.CurrentProject(newEnvironment("glab").glab(), origin)
	if err != nil {
		return "", "", err
	}
	return scaffold.GitLab(project.Host, project.Path), project.String(), nil
}

// origin は cwd の clone の origin の URL。
func (e environment) origin() (string, error) {
	git, err := deps.Lookup("git", e.env)
	if err != nil {
		return "", err
	}
	out, err := proc.Command{Path: git, Env: e.env, Timeout: commandTimeout}.Output("remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("cwd で origin の URL を読めない: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// remoteHost は git の remote の URL から host を読む。読むのは `https://<host>/…`・`ssh://<user>@<host>[:<port>]/…`・
// `<user>@<host>:…` (scp の綴り)。port を除いて小文字にする。URL は認証情報を含みうるので、誤りに URL を載せない。
func remoteHost(remote string) (string, error) {
	var host string
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil {
			// url.Parse の誤りは URL をそのまま持つので捨てる
			return "", errors.New("URL として読めない")
		}
		host = u.Hostname()
	} else if before, _, ok := strings.Cut(remote, ":"); ok && !strings.Contains(before, "/") {
		// scp の綴り。`:` より前に `/` があるものは local の path
		host = before
		if _, after, ok := strings.Cut(before, "@"); ok {
			host = after
		}
	}
	if host == "" {
		return "", errors.New("host を読めない (https://・ssh://・user@host: の綴りの URL にする)")
	}
	return strings.ToLower(host), nil
}
