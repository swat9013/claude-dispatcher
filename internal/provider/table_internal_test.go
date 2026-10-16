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

func TestEveryProviderDeclaresTheHostAndTheSetupConsistently(t *testing.T) {
	for _, p := range providers {
		t.Run(string(p.Kind), func(t *testing.T) {
			if host, ok := p.Host.Default(); !ok && host != "" {
				t.Fatalf("既定を持たないのに既定の host %q がある", host)
			}
			if p.Host.Required() && p.Host.RequiredHint() == "" {
				t.Fatalf("tracker.host は必須なのに、欠落の誤りに添える案内が無い")
			}
			if p.Setup.DeclaresOrigin() && (!p.Setup.DeclaresCurrentPlace() || !p.Setup.DeclaresTemplate()) {
				t.Fatalf("origin を認めるのに、clone から雛形を書けない")
			}
			if p.Setup.TakesFlags() && (!p.Setup.DeclaresTemplate() || !p.Host.Writable()) {
				t.Fatalf("flag で受けるのに、--host を読めないか雛形を書けない")
			}
		})
	}
}

func TestTheProviderForAnUnrecognizedOriginWritesTheTemplateFromTheClone(t *testing.T) {
	p, _ := Of(originFallback)

	if !p.Setup.DeclaresCurrentPlace() || !p.Setup.DeclaresTemplate() {
		t.Fatalf("origin の host を認める provider が無いときの種類 %s が、clone から雛形を書けない", p.Kind)
	}
}

func TestEveryProviderReturnsTheScopeKeyFromItsPlace(t *testing.T) {
	for _, p := range providers {
		if p.scopeKey == nil {
			t.Errorf("%s の provider は scope key の決め方を持たない", p.Kind)
		}
	}
}
