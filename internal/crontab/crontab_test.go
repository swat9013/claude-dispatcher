package crontab_test

import (
	"slices"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/crontab"
)

func TestTickLinesPicksOnlyTheProjectsUncommentedTickLines(t *testing.T) {
	table := "# */5 * * * * cd /x && /bin/claude-dispatcher tick widgets\n" +
		"*/5 * * * * cd /x && /bin/claude-dispatcher tick widgets >> /s/cron.log 2>&1\n" +
		"*/5 * * * * cd /y && /bin/claude-dispatcher tick widgets2 >> /s2/cron.log 2>&1\n" +
		"0 3 * * * /usr/bin/backup\n"

	got := crontab.TickLines(table, "widgets")

	if want := []string{"*/5 * * * * cd /x && /bin/claude-dispatcher tick widgets >> /s/cron.log 2>&1"}; !slices.Equal(got, want) {
		t.Fatalf("TickLines = %q", got)
	}
}

func TestLineQuotesPathsThatTheShellWouldSplit(t *testing.T) {
	got := crontab.Line("/home/me/my repo", "/bin/claude-dispatcher", "widgets", "/s/it's/cron.log")

	want := `*/5 * * * * cd '/home/me/my repo' && /bin/claude-dispatcher tick widgets >> '/s/it'\''s/cron.log' 2>&1`
	if got != want {
		t.Fatalf("Line = %s\nwant   %s", got, want)
	}
}

func TestAppendKeepsTheCurrentLines(t *testing.T) {
	for _, tc := range []struct{ table, want string }{
		{"", "L\n"},
		{"0 3 * * * /usr/bin/backup", "0 3 * * * /usr/bin/backup\nL\n"},
		{"0 3 * * * /usr/bin/backup\n", "0 3 * * * /usr/bin/backup\nL\n"},
	} {
		if got := crontab.Append(tc.table, "L"); got != tc.want {
			t.Fatalf("Append(%q) = %q, want %q", tc.table, got, tc.want)
		}
	}
}

func TestTickLinesCountsTheLineOfTheScriptThisWasPortedFrom(t *testing.T) {
	legacy := "*/5 * * * * cd /x && PATH=/u:$PATH ~/.claude/skills/swat-skills/skills/util/dispatcher/scripts/dispatcher-tick.py widgets >> ~/.claude/dispatcher/widgets/cron.log 2>&1"

	got := crontab.TickLines(legacy+"\n", "widgets")

	if !slices.Equal(got, []string{legacy}) {
		t.Fatalf("TickLines = %q", got)
	}
}

func TestSameCommandIgnoresTheSchedule(t *testing.T) {
	line := crontab.Line("/x", "/bin/claude-dispatcher", "widgets", "/s/cron.log")
	for _, tc := range []struct {
		other string
		want  bool
	}{
		{"*/10 * * * *  cd /x && /bin/claude-dispatcher tick widgets >> /s/cron.log 2>&1", true},
		{"*/5 * * * * cd /y && /bin/claude-dispatcher tick widgets >> /s/cron.log 2>&1", false},
		{"*/5 * * * * cd /x && /bin/claude-dispatcher tick widgets", false},
	} {
		if got := crontab.SameCommand(tc.other, line); got != tc.want {
			t.Fatalf("SameCommand(%q) = %v, want %v", tc.other, got, tc.want)
		}
	}
}

func TestLineEscapesPercentSignsThatCronWouldTurnIntoNewlines(t *testing.T) {
	got := crontab.Line("/home/me/100%work", "/bin/claude-dispatcher", "widgets", "/s/cron.log")

	want := `*/5 * * * * cd '/home/me/100\%work' && /bin/claude-dispatcher tick widgets >> /s/cron.log 2>&1`
	if got != want {
		t.Fatalf("Line = %s\nwant   %s", got, want)
	}
}
