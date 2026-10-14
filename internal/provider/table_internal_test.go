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
