// Package stubwire は black-box テストの harness と stub (test/blackbox/stub) の間の取り決め。
//
// harness が応答 rule を書き、stub が読んで応答し、stub が呼び出しを記録し、harness が読む。両側が同じ理由で
// 一緒に変わるので、置き場と JSON の形をここ 1 箇所に置く。テスト対象の binary とは関係しない。
//
// stub root の下:
//
//	calls/<name>/<unixnano>-<pid>.json  呼び出し 1 回の記録 (Call)
//	responses/<name>.json               応答の rule 列 ([]Rule)。先頭から見て最初に当たった rule で応答する
//	crontab                             crontab の fake が持つ表 (無ければ表が無い)
package stubwire

import (
	"path/filepath"
	"strconv"
)

// RootFile は stub binary の隣に置き、stub root の path を 1 行で持つ file の名前。
// root を env ではなく file で渡すのは、`tick --dry-run --cron-env` が env を剥がして撃ち直すため。
const RootFile = ".stub-root"

// UnmatchedExit は、どの rule にも当たらない呼び出しに stub が返す exit code。
// 想定外の呼び出しを成功に化けさせず、テストの失敗として表に出す。
const UnmatchedExit = 98

func CallsDir(root, name string) string { return filepath.Join(root, "calls", name) }

// CrontabFile は crontab の fake が持つ表の置き場。crontab は rule で応答する stub ではなく、
// `crontab -` で入った表を `crontab -l` が返す fake にする (登録した後に読み直す手順を実物どおりに通すため)。
func CrontabFile(root string) string { return filepath.Join(root, "crontab") }

func ResponsesFile(root, name string) string {
	return filepath.Join(root, "responses", name+".json")
}

// Call は stub の呼び出し 1 回の記録。
type Call struct {
	// Exe は起動された実行 file の path (PATH のどこから引かれたかを見る)
	Exe  string            `json:"exe"`
	Argv []string          `json:"argv"`
	Cwd  string            `json:"cwd"`
	Env  map[string]string `json:"env"`
	// Stdin は `crontab -` が受け取った表 (他の呼び出しでは読まない)
	Stdin string `json:"stdin,omitempty"`
}

// RecordedEnv は Call に残す env。token の受け渡しと置き場の解決を assert するためのもの。
var RecordedEnv = []string{
	"GH_TOKEN", "GITHUB_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN",
	"HOME", "PATH", "XDG_CONFIG_HOME", "XDG_STATE_HOME",
}

// Rule は応答 1 つ。ArgsPrefix と ArgContains の両方に当たった呼び出しに応答する。
type Rule struct {
	// ArgsPrefix は argv[1:] の先頭一致。空なら何にでも当たる
	ArgsPrefix []string `json:"args_prefix,omitempty"`
	// ArgContains は argv[1:] のどれかが含む部分文字列。空なら条件にしない
	ArgContains string `json:"arg_contains,omitempty"`
	Stdout      string `json:"stdout,omitempty"`
	Stderr      string `json:"stderr,omitempty"`
	Exit        int    `json:"exit,omitempty"`
	// Decisions は orchestrator の代役: state dir の最新の指示ファイルと同じ stem で決定ファイルを書く
	Decisions *DecisionsWrite `json:"decisions,omitempty"`
}

// DecisionsWrite は orchestrator の代役が書くもの。
type DecisionsWrite struct {
	StateDir string `json:"state_dir"`
	Content  string `json:"content"`
	// ObstructWorkerLogs は、決定ファイルと一緒に worker log の path を dir で塞ぐ issue 番号。
	// CLI がその worker の log を開けずに止まる状況を、他の worker を起動させた後に作る
	ObstructWorkerLogs []int `json:"obstruct_worker_logs,omitempty"`
}

func InstructionsGlob(stateDir string) string {
	return filepath.Join(stateDir, "instructions", "*.json")
}

func DecisionsFile(stateDir, stem string) string {
	return filepath.Join(stateDir, "decisions", stem+".json")
}

func WorkerLogFile(stateDir string, issue int, stem string) string {
	return filepath.Join(stateDir, "workers", strconv.Itoa(issue)+"-"+stem+".log")
}
