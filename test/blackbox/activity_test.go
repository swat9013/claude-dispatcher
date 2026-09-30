package blackbox_test

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// status の表の ACTIVITY 列 (formats.md §10)。Claude Code の transcript (`<設定 dir>/projects/*/<session_id>.jsonl`) の
// 末尾から、最後の event からの経過と最後の tool 呼び出し (か、その後の発話の頭) を出す。

// transcriptEvent は transcript の 1 行。age は今から何秒前の event か。
type transcriptEvent struct {
	age     time.Duration
	content []map[string]any
}

func toolUse(name string, input map[string]any) map[string]any {
	return map[string]any{"type": "tool_use", "id": "t", "name": name, "input": input}
}

func text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

// writeTranscript は configDir の下に sessionID の transcript を置く (dir 名は Claude Code が cwd から作るものに似せた任意の綴り)。
func writeTranscript(t *testing.T, configDir, sessionID string, events ...transcriptEvent) {
	t.Helper()
	var lines []string
	for _, e := range events {
		lines = append(lines, mustJSON(t, map[string]any{
			"type": "assistant", "sessionId": sessionID, "timestamp": time.Now().Add(-e.age).UTC().Format(time.RFC3339Nano),
			"message": map[string]any{"role": "assistant", "content": e.content},
		}))
	}
	// 末尾に timestamp を持たないメタデータの行が続く (実物の transcript と同じ)
	lines = append(lines, mustJSON(t, map[string]any{"type": "last-prompt", "sessionId": sessionID}))
	dir := filepath.Join(configDir, "projects", "-somewhere-clone")
	mustMkdir(t, dir)
	mustWrite(t, filepath.Join(dir, sessionID+".jsonl"), strings.Join(lines, "\n")+"\n")
}

func (s *sandbox) defaultClaudeConfigDir() string { return filepath.Join(s.home, ".claude") }

// runningWorkerScenario は issue 42 の worker が走っている状況を作る。
func (s *sandbox) runningWorkerScenario() {
	s.statusScenario()
	s.workerAlive()
	s.setWip(42)
}

// activityCell は #42 の行の ACTIVITY (TICK の後ろの全部)。
func activityCell(t *testing.T, stdout string) string {
	t.Helper()
	row := workerRow(stdout, 42)
	for i, field := range row {
		if regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`).MatchString(field) {
			return strings.Join(row[i+1:], " ")
		}
	}
	t.Fatalf("#42 の行に TICK が無い: %v\n%s", row, stdout)
	return ""
}

func TestActivityShowsTheLastToolCallWithItsElapsedTime(t *testing.T) {
	s := newSandbox(t)
	s.runningWorkerScenario()
	writeTranscript(t, s.defaultClaudeConfigDir(), workerSession,
		transcriptEvent{age: time.Minute, content: []map[string]any{text("テストを撃つ")}},
		transcriptEvent{age: 12 * time.Second, content: []map[string]any{toolUse("Bash", map[string]any{"command": "go test ./...\necho done"})}},
	)

	r := s.statusPS()

	if got := activityCell(t, r.stdout); !regexp.MustCompile(`^1\ds Bash go test \./\.\.\.$`).MatchString(got) {
		t.Fatalf("ACTIVITY = %q, want `12s Bash go test ./...` (経過・tool 名・command の 1 行目)\n%s", got, r.stdout)
	}
}

func TestActivityShowsTheHeadOfAnUtteranceAfterTheLastToolCall(t *testing.T) {
	s := newSandbox(t)
	s.runningWorkerScenario()
	writeTranscript(t, s.defaultClaudeConfigDir(), workerSession,
		transcriptEvent{age: 5 * time.Minute, content: []map[string]any{toolUse("Edit", map[string]any{"file_path": "internal/x.go"})}},
		transcriptEvent{age: 3 * time.Minute, content: []map[string]any{text("直したので PR を作る\n続きの行")}},
	)

	r := s.statusPS()

	if got := activityCell(t, r.stdout); got != "3m 直したので PR を作る" {
		t.Fatalf("ACTIVITY = %q, want `3m 直したので PR を作る` (tool 呼び出しの後の発話の頭)\n%s", got, r.stdout)
	}
}

func TestActivityIsUnknownWithANoteWhenTheTranscriptIsMissing(t *testing.T) {
	s := newSandbox(t)
	s.runningWorkerScenario()

	r := s.statusPS()

	row := strings.Join(workerRow(r.stdout, 42), " ")
	if got := activityCell(t, r.stdout); got != "?" {
		t.Fatalf("ACTIVITY = %q, want ?\n%s", got, r.stdout)
	}
	for _, want := range []string{"start", "running", "+3", "yes", "#57 OPEN"} {
		if !strings.Contains(row, want) {
			t.Fatalf("transcript が無いだけで他の列 %q が変わった: %s", want, row)
		}
	}
	if !strings.Contains(r.stdout, "  ! #42 の transcript を読めない") {
		t.Fatalf("transcript を読めない注記が無い:\n%s", r.stdout)
	}
}

func TestActivityLooksUnderClaudeConfigDirWhenItIsSet(t *testing.T) {
	s := newSandbox(t)
	s.runningWorkerScenario()
	configDir := filepath.Join(s.root, "claude-config")
	writeTranscript(t, configDir, workerSession,
		transcriptEvent{age: 30 * time.Second, content: []map[string]any{toolUse("Read", map[string]any{"file_path": "docs/design/formats.md"})}},
	)

	r := s.runWithEnv(map[string]string{"CLAUDE_CONFIG_DIR": configDir}, "status", "ps", s.project)

	if got := activityCell(t, r.stdout); !regexp.MustCompile(`^3\ds Read docs/design/formats\.md$`).MatchString(got) {
		t.Fatalf("ACTIVITY = %q, want CLAUDE_CONFIG_DIR の下の transcript から `30s Read docs/design/formats.md`\n%s", got, r.stdout)
	}
}

func TestActivityIsADashForAWorkerThatIsNotRunning(t *testing.T) {
	s := newSandbox(t)
	s.statusScenario()
	s.setProcesses()
	s.setWip(42)
	writeTranscript(t, s.defaultClaudeConfigDir(), workerSession,
		transcriptEvent{age: time.Hour, content: []map[string]any{toolUse("Bash", map[string]any{"command": "make"})}},
	)

	r := s.statusPS()

	if got := activityCell(t, r.stdout); got != "-" {
		t.Fatalf("ACTIVITY = %q, want - (running でない行)\n%s", got, r.stdout)
	}
}

func TestActivityCutsTheContentTo60CharactersWhenStdoutIsNotATerminal(t *testing.T) {
	s := newSandbox(t)
	s.runningWorkerScenario()
	long := strings.Repeat("x", 100)
	writeTranscript(t, s.defaultClaudeConfigDir(), workerSession,
		transcriptEvent{age: 2 * time.Second, content: []map[string]any{text(long)}},
	)

	r := s.statusPS()

	if got, want := activityCell(t, r.stdout), strings.Repeat("x", 60); !strings.HasSuffix(got, " "+want) || strings.Contains(got, want+"x") {
		t.Fatalf("ACTIVITY = %q, want 内容を 60 文字で切ったもの", got)
	}
}

func TestActivityOfTheOrchestratorRow(t *testing.T) {
	s := newSandbox(t)
	s.setIssues(readyIssue(42))
	s.orchestratorWaitsAfterWriting(startDecisions(playbookPath(s.defaultInstallPath(), "playbook-implementation"), 42))
	s.t.Cleanup(func() { s.release("orchestrator") })
	p := s.startLoop()
	sessionID, _ := s.waitOrchestratorCalls(1)[0].flagValue("--session-id")
	s.agentsListOrchestrator(sessionID)
	s.dispatcherProcess(p.cmd.Process.Pid, "loop", s.project, loopInterval)
	writeTranscript(t, s.defaultClaudeConfigDir(), sessionID,
		transcriptEvent{age: 4 * time.Second, content: []map[string]any{toolUse("Bash", map[string]any{"command": "gh issue list"})}},
	)

	r := s.statusPS()

	if row := strings.Join(orchestratorRow(r.stdout), " "); !regexp.MustCompile(`Z \ds Bash gh issue list$`).MatchString(row) {
		t.Fatalf("orchestrator の行 = %q, want ACTIVITY `4s Bash gh issue list`\n%s", row, r.stdout)
	}
}
