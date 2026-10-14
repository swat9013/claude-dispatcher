package provider

import (
	"slices"
	"testing"
)

func TestNoProviderWritesTrackerTokenInTheSupportsOfItsTrackerRole(t *testing.T) {
	for _, p := range providers {
		if slices.Contains(p.Tracker.declared.Supports, Token) {
			t.Fatalf("provider %s が Supports に tracker.token を書いている (TokenVariable から導く)", p.Kind)
		}
	}
}

func TestAnEmptyDefaultHostIsADefaultSoTheHostIsNotRequired(t *testing.T) {
	h := Host{parse: func(s string) (string, error) { return s, nil }, defaultHost: "", hasDefault: true}

	host, ok := h.Default()

	if !ok || host != "" || h.Required() {
		t.Fatalf("Default() = (%q, %v), Required() = %v, want 空文字の既定を持ち必須でない", host, ok, h.Required())
	}
}

func TestAWritableHostWithoutADefaultIsRequired(t *testing.T) {
	h := Host{parse: func(s string) (string, error) { return s, nil }}

	_, ok := h.Default()

	if ok || !h.Required() {
		t.Fatalf("Default() の有無 = %v, Required() = %v, want 既定を持たず必須", ok, h.Required())
	}
}

func TestParsingTheHostOfAKindThatCannotWriteItIsAnError(t *testing.T) {
	var h Host

	_, err := h.Parse("github.com")

	if err == nil {
		t.Fatal("tracker.host を書けない種類の host を読めた")
	}
}

func TestEveryProviderDeclaresTheHostAndTheSetupConsistently(t *testing.T) {
	for _, p := range providers {
		t.Run(string(p.Kind), func(t *testing.T) {
			if !p.Host.hasDefault && p.Host.defaultHost != "" {
				t.Fatalf("既定を持たないのに既定の host %q がある", p.Host.defaultHost)
			}
			if p.Host.Required() && p.Host.requiredHint == "" {
				t.Fatalf("tracker.host は必須なのに、欠落の誤りに添える案内が無い")
			}
			if p.Setup.ownsOrigin != nil && (p.Setup.currentPlace == nil || p.Setup.template == nil) {
				t.Fatalf("origin を認めるのに、clone から雛形を書けない")
			}
			if p.Setup.TakesFlags() && (p.Setup.template == nil || !p.Host.Writable()) {
				t.Fatalf("flag で受けるのに、--host を読めないか雛形を書けない")
			}
		})
	}
}

func TestTheProviderForAnUnrecognizedOriginWritesTheTemplateFromTheClone(t *testing.T) {
	p, _ := Of(originFallback)

	if p.Setup.currentPlace == nil || p.Setup.template == nil {
		t.Fatalf("origin の host を認める provider が無いときの種類 %s が、clone から雛形を書けない", p.Kind)
	}
}
