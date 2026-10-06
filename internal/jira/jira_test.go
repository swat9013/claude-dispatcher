package jira_test

import (
	"slices"
	"testing"

	"github.com/swat9013/claude-dispatcher/internal/jira"
)

// fakeAcli は撃たれた argv を残し、auth status にだけ site を返して他は空の一覧で応える。
type fakeAcli struct {
	calls [][]string
}

func (f *fakeAcli) Run(args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	switch {
	case slices.Equal(args, []string{"jira", "auth", "status"}):
		return []byte("✓ Authenticated\n  Site: acme.atlassian.net\n"), nil
	case slices.Contains(args, "--count"):
		return []byte("✓ Number of work items in the search: 0\n"), nil
	}
	return []byte("[]"), nil
}

func TestStatusNameAndProjectKeyAreQuotedAsJQLStrings(t *testing.T) {
	acli := &fakeAcli{}
	store := jira.NewStore(acli, jira.Project{Site: "acme.atlassian.net", Key: "SET"}, nil)

	err := store.Confirm(jira.Names{Statuses: []jira.Name{{Trigger: "t", Value: `Say "hi" \ bye`}}})

	if err != nil {
		t.Fatal(err)
	}
	want := `project = "SET" AND status = "Say \"hi\" \\ bye"`
	if !slices.ContainsFunc(acli.calls, func(args []string) bool { return slices.Contains(args, want) }) {
		t.Fatalf("JQL %q を撃っていない: %q", want, acli.calls)
	}
}

func TestProjectKeyIsReadInUpperCase(t *testing.T) {
	for _, text := range []string{"widgets", "W1_2"} {
		if _, err := jira.ParseKey(text); err != nil {
			t.Errorf("ParseKey(%q) = %v", text, err)
		}
	}
	if key, _ := jira.ParseKey("widgets"); key != "WIDGETS" {
		t.Errorf("ParseKey(widgets) = %q, want WIDGETS", key)
	}
}

func TestProjectKeyOutsideTheSpellingIsRejected(t *testing.T) {
	for _, text := range []string{"1W", "acme/widgets", "W-1"} {
		if _, err := jira.ParseKey(text); err == nil {
			t.Errorf("ParseKey(%q) を通した", text)
		}
	}
}

func TestSiteIsAHostName(t *testing.T) {
	if site, err := jira.ParseSite("acme.atlassian.net"); err != nil || site != "acme.atlassian.net" {
		t.Errorf("ParseSite(acme.atlassian.net) = %q, %v", site, err)
	}
	for _, text := range []string{"https://acme.atlassian.net", "acme.atlassian.net/", "acme atlassian"} {
		if _, err := jira.ParseSite(text); err == nil {
			t.Errorf("ParseSite(%q) を通した", text)
		}
	}
}
