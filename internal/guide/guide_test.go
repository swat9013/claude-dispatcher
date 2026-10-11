package guide

import (
	"regexp"
	"testing"
)

// unavailable は利用者の手元に無いものを指す綴り: repo の設計 doc・実装の dir・ADR・issue 番号 (ADR 0012)
var unavailable = regexp.MustCompile(`system\.md|formats\.md|docs/|internal/|ADR|#[0-9]+`)

func TestGuideDoesNotPointToWhatTheUserDoesNotHave(t *testing.T) {
	text := Text()

	found := unavailable.FindAllString(text, -1)

	if len(found) > 0 {
		t.Fatalf("案内が利用者の手元に無いものを指している: %q", found)
	}
}
