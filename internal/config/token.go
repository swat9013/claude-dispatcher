package config

import (
	"os"
	"strings"
)

// gh が認証に読む環境変数。どちらかがあれば token_file は読まない (env が勝つ)
var ghTokenEnvs = []string{"GH_TOKEN", "GITHUB_TOKEN"}

// ClaudeTokenEnv は claude が長期 token を読む環境変数
const ClaudeTokenEnv = "CLAUDE_CODE_OAUTH_TOKEN"

// TokenEnv は子プロセスへ足す env (system.md §8)。gh には GH だけを、claude には両方を渡す。
type TokenEnv struct {
	GH     map[string]string
	Claude map[string]string
}

// Tokens は token file から子プロセスへ足す env を返す。
// env に gh の token が無ければ token_file の中身を GH_TOKEN として、env に claude の token が無ければ
// claude_token_file の中身を CLAUDE_CODE_OAUTH_TOKEN として載せる。env にあればその file は読みもしない。
func (c Config) Tokens(getenv func(string) string) (TokenEnv, error) {
	tokens := TokenEnv{GH: map[string]string{}, Claude: map[string]string{}}
	if c.TokenFile != "" && getenv(ghTokenEnvs[0]) == "" && getenv(ghTokenEnvs[1]) == "" {
		token, err := c.readToken("token_file", c.TokenFile)
		if err != nil {
			return TokenEnv{}, err
		}
		// orchestrator / worker も gh を撃つので、claude にも同じ token を渡す
		tokens.GH["GH_TOKEN"] = token
		tokens.Claude["GH_TOKEN"] = token
	}
	if c.ClaudeTokenFile != "" && getenv(ClaudeTokenEnv) == "" {
		token, err := c.readToken("claude_token_file", c.ClaudeTokenFile)
		if err != nil {
			return TokenEnv{}, err
		}
		tokens.Claude[ClaudeTokenEnv] = token
	}
	return tokens, nil
}

// readToken は token file の中身を空白を除いて返す。token 自体はどの error 文にも出さない。
//
// 空白は端々だけでなく途中も除く — 端末で折り返された token を貼ると途中に改行が入り、改行入りの token では
// 認証 header を組めない。
func (c Config) readToken(key, file string) (string, error) {
	info, err := os.Stat(file)
	if err != nil {
		return "", errorf("%s: [auth].%s を読めない: %s (%v)", c.Path, key, file, err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return "", errorf("%s: [auth].%s が group / other から読める mode (%#o): %s。`chmod 600` にする", c.Path, key, mode, file)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", errorf("%s: [auth].%s を読めない: %s (%v)", c.Path, key, file, err)
	}
	token := strings.Join(strings.Fields(string(raw)), "")
	if token == "" {
		return "", errorf("%s: [auth].%s が空: %s", c.Path, key, file)
	}
	return token, nil
}
