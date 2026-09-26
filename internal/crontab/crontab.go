// Package crontab は tick を撃つ crontab の行 (formats.md §11) を組み、ユーザーの crontab を `crontab` コマンドで読み書きする。
package crontab

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/swat9013/claude-dispatcher/internal/proc"
)

// Schedule は tick の周期。orchestrator の上限 (15 分) より短くても lock で重ならない
const Schedule = "*/5 * * * *"

// Line は project の tick を撃つ crontab の 1 行。cron の PATH に binary は無いので self は絶対 path で書く (system.md §9)。
func Line(clone, self, project, cronLog string) string {
	command := fmt.Sprintf("cd %s && %s tick %s >> %s 2>&1", shellQuote(clone), shellQuote(self), project, shellQuote(cronLog))
	// cron は command の中の % を (quote の中でも) 改行に変えるので、\% で渡す
	return Schedule + " " + strings.ReplaceAll(command, "%", `\%`)
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_./~:@+=,-]+$`)

// shellQuote は shell が 1 語として読む形にする。特殊な文字が無ければそのまま (人が crontab を読みやすいように)。
func shellQuote(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	const quote = "'"
	return quote + strings.ReplaceAll(s, quote, quote+`\`+quote+quote) + quote
}

// TickLines は table のうち project の tick を撃つ行 (コメントでない行) を返す。`tick <project>` の行に加えて、
// 移植元の `dispatcher-tick.py <project>` の行も数える — 見落とすと隣に行を足し、同じ project を 2 重に tick する。
// 移植元の行を数えるのは移行のあいだだけ。移植元の script (swat-skills の dispatcher) が廃止されたら外す。
func TickLines(table, project string) []string {
	pattern := regexp.MustCompile(`(^|[\s/])(tick|dispatcher-tick\.py)\s+` + regexp.QuoteMeta(project) + `(\s|$)`)
	var lines []string
	for _, line := range strings.Split(table, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if pattern.MatchString(trimmed) {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

// SameCommand は 2 つの crontab の行が周期の欄 (先頭 5 欄) を除いて同じか。周期は人が変えてよいので、setup が組む行との
// 突き合わせは command (cd 先・binary・cron.log) だけで見る。
func SameCommand(a, b string) bool {
	command := func(line string) string {
		fields := strings.Fields(line)
		if len(fields) <= 5 {
			return ""
		}
		return strings.Join(fields[5:], " ")
	}
	return command(a) != "" && command(a) == command(b)
}

// Append は table の末尾に line を足した表を返す。
func Append(table, line string) string {
	if table != "" && !strings.HasSuffix(table, "\n") {
		table += "\n"
	}
	return table + line + "\n"
}

// Client はユーザーの crontab を `crontab` コマンドで読み書きする。
type Client struct{ Command proc.Command }

// Read は今の表を返す。表が無ければ "" (`crontab -l` は "no crontab for" を出して exit 1 で終わる)。
func (c Client) Read() (string, error) {
	out, err := c.Command.Output("-l")
	var failed *proc.Error
	if errors.As(err, &failed) && failed.Exit == 1 && strings.Contains(failed.Stderr, "no crontab") {
		return "", nil
	}
	return out, err
}

// Write は表を丸ごと置き換える。`crontab` は表全体を受け取るので、現行の行を落とさないよう呼び出し側が全体を渡す
// (file の path で渡すと macOS の crontab は長い path を途中で切るので stdin で渡す)。
func (c Client) Write(table string) error {
	_, err := c.Command.OutputWithInput(table, "-")
	return err
}
