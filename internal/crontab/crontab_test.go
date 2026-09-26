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
	for _, table := range []string{"", "0 3 * * * /usr/bin/backup", "0 3 * * * /usr/bin/backup\n"} {
		got := crontab.Append(table, "L")

		want := "L\n"
		if table != "" {
			want = "0 3 * * * /usr/bin/backup\nL\n"
		}
		if got != want {
			t.Fatalf("Append(%q) = %q", table, got)
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
