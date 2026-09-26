// Package contract は orchestrator の契約 file を binary に埋め込み、tick ごとの prompt を組む (ADR 0003)。
//
// 契約は skill として登録せず prompt として直接渡すので、契約と tick の版が必ず一致する。
package contract

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/swat9013/claude-dispatcher/internal/config"
)

//go:embed orchestrator.md
var orchestrator string

// Text は埋め込んだ契約の原文 (差し込み前)。
func Text() string { return orchestrator }

// Params は契約に差し込む tick ごとの値。path はすべて絶対 path。
type Params struct {
	InstructionFile string
	DecisionsFile   string
	// HandoffFile は人へ返す引き渡し本文の置き場。issue 番号の位置を `<N>` で書く
	HandoffFile    string
	IssueRepo      string
	CLRepo         string
	ReadyLabel     string
	TriageLabel    string // 空なら残タスクの起票に label を付けない
	PrincipleIndex string
}

// Prompt は params を差し込んだ orchestrator の prompt を返す。
func Prompt(p Params) (string, error) {
	triage := "起票には label を付けない (宣言 config に triage label が無い)。"
	if p.TriageLabel != "" {
		triage = fmt.Sprintf("起票には triage label `%s` だけを付ける。", p.TriageLabel)
	}
	prompt := strings.NewReplacer(
		"<<INSTRUCTION_FILE>>", p.InstructionFile,
		"<<DECISIONS_FILE>>", p.DecisionsFile,
		"<<HANDOFF_FILE>>", p.HandoffFile,
		"<<ISSUE_REPO>>", p.IssueRepo,
		"<<CL_REPO>>", p.CLRepo,
		"<<READY_LABEL>>", p.ReadyLabel,
		"<<WIP_LABEL>>", config.WIPLabel,
		"<<HUMAN_LABEL>>", config.HumanLabel,
		"<<TRIAGE_LABEL_RULE>>", triage,
		"<<PRINCIPLE_INDEX>>", p.PrincipleIndex,
	).Replace(orchestrator)
	if i := strings.Index(prompt, "<<"); i >= 0 {
		end := min(len(prompt), i+40)
		return "", fmt.Errorf("契約に差し込まれていない値が残っている: %q", prompt[i:end])
	}
	return prompt, nil
}
