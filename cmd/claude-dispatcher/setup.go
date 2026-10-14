package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/swat9013/claude-dispatcher/internal/deps"
	"github.com/swat9013/claude-dispatcher/internal/proc"
	"github.com/swat9013/claude-dispatcher/internal/provider"
)

// runSetup は `setup [--kind jira --host <site> --repo <project key>] [<workflow の path>]` を撃つ (formats.md §7.4):
// flag があれば jira の雛形を、無ければ cwd の clone の origin から tracker.kind と issue 置き場を決めて、その種類の雛形を書く。
// CL 置き場は、どちらも cwd の clone の origin から決める (jira は cl: に書く)。既にある file は上書きしない。
func runSetup(args []string, stdout, stderr io.Writer) int {
	flags, rest, err := setupFlags(args)
	if err != nil {
		return usageError(stderr, "%v", err)
	}
	path, ok := workflowArg(rest, nil, stderr)
	if !ok {
		return exitUsage
	}
	// flag の綴りは、file の有無に依らず確かめる
	var flagged *flaggedPlace
	if flags.kind != "" {
		f, err := flags.place()
		if err != nil {
			return usageError(stderr, "%v", err)
		}
		flagged = &f
	}
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(stdout, "%s は既にあるので書かない\n", path)
		return 0
	}
	origin, originPlace, err := placeOfOrigin()
	if err != nil {
		what := "issue 置き場"
		if flagged != nil {
			what = "CL 置き場"
		}
		fmt.Fprintf(stderr, "%s を決められない: %v\n", what, err)
		return exitFailed
	}
	tracker, place, placeNote := origin, originPlace, "issue 置き場: "+originPlace.String()
	if flagged != nil {
		tracker, place = flagged.provider, flagged.place
		placeNote = "issue 置き場: " + place.String() + " · CL 置き場: " + originPlace.String()
	}
	template := tracker.Setup.Template(place, origin.CLHost.Scaffold(originPlace))
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
	fmt.Fprintf(stdout, "%s に workflow 定義の雛形を書いた (%s)。project に合わせて直し、`claude-dispatcher loop --dry-run` で試運転する\n", path, placeNote)
	return 0
}

// setupOptions は setup の flag (`--kind <種類> --host <host> --repo <repo>`)。
type setupOptions struct {
	kind, host, repo string
}

// setupFlags は args から setup の flag を抜き出し、残りの引数を返す。`--name value` と `--name=value` を読む。
func setupFlags(args []string) (setupOptions, []string, error) {
	var o setupOptions
	var rest []string
	targets := map[string]*string{"--kind": &o.kind, "--host": &o.host, "--repo": &o.repo}
	for i := 0; i < len(args); i++ {
		name, value, inline := strings.Cut(args[i], "=")
		target, known := targets[name]
		if !known {
			rest = append(rest, args[i])
			continue
		}
		if !inline {
			if i+1 >= len(args) {
				return o, nil, fmt.Errorf("%s に値が要る", name)
			}
			i++
			value = args[i]
		}
		if *target != "" {
			return o, nil, fmt.Errorf("%s を 2 回書いている", name)
		}
		if value == "" {
			return o, nil, fmt.Errorf("%s の値が空", name)
		}
		*target = value
	}
	if o.kind == "" && (o.host != "" || o.repo != "") {
		return o, nil, fmt.Errorf("--host と --repo は --kind %s と一緒にだけ書ける", flagKinds())
	}
	return o, rest, nil
}

// flaggedPlace は flag から読んだ置き場と、その種類の provider。
type flaggedPlace struct {
	provider provider.Provider
	place    provider.Place
}

// flagKinds は `setup --kind` で受ける種類を誤りの文言の綴りで並べる。
func flagKinds() string { return provider.Join(provider.SetupFlagKinds()) }

// place は --kind の flag から、その種類の provider の読み方で置き場を読む。flag で受けるのは provider が宣言した種類だけ。
func (o setupOptions) place() (flaggedPlace, error) {
	p, ok := provider.Of(provider.Kind(o.kind))
	if !ok || p.Setup.Flags == "" {
		return flaggedPlace{}, fmt.Errorf("--kind は %s だけを受ける (%q)", flagKinds(), o.kind)
	}
	if o.host == "" || o.repo == "" {
		return flaggedPlace{}, fmt.Errorf("--kind %s には %s が要る", p.Kind, p.Setup.Flags)
	}
	host, err := p.Host.Parse(o.host)
	if err != nil {
		return flaggedPlace{}, fmt.Errorf("--host %q: %w", o.host, err)
	}
	repo, err := p.ParseRepo(o.repo)
	if err != nil {
		return flaggedPlace{}, fmt.Errorf("--repo %q: %w", o.repo, err)
	}
	return flaggedPlace{provider: p, place: p.NewPlace(host, repo)}, nil
}

// placeOfOrigin は cwd の clone の origin の host を認める provider (host が github.com なら github、それ以外は gitlab) と、
// その provider の CLI で引いた clone の置き場を返す。PATH は撃つ CLI ごとに解決する (git の後、host で決まった CLI だけ)。
func placeOfOrigin() (provider.Provider, provider.Place, error) {
	origin, err := commandEnvironment("git").origin()
	if err != nil {
		return provider.Provider{}, provider.Place{}, err
	}
	host, err := remoteHost(origin)
	if err != nil {
		// URL は認証情報を含みうるので名指しに載せない
		return provider.Provider{}, provider.Place{}, fmt.Errorf("origin の URL: %w", err)
	}
	p := provider.ForOrigin(host)
	current, err := p.Setup.CurrentPlace(commandEnvironment(p.Command).runtime())
	return p, current, err
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
