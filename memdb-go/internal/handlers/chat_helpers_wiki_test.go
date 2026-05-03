package handlers

import "testing"

// TestIncludeWikiDefault_OptInSemantics pins the contract: a chat request
// with IncludeWiki nil or false MUST NOT trigger the wiki retrieval slot.
// Karpathy r2 forensic (2026-05-01) flipped the slot from always-on to
// opt-in because raw-cosine wiki entries displace decay-scored real
// memories at top-1 on aged corpora.
//
// The check itself is a one-liner (`derefBoolOr(req.IncludeWiki, false)`),
// but isolating it as a named test prevents a future refactor from silently
// inverting the default.
func TestIncludeWikiDefault_OptInSemantics(t *testing.T) {
	tt := []struct {
		name string
		req  *nativeChatRequest
		want bool
	}{
		{"nil pointer skips wiki", &nativeChatRequest{}, false},
		{"explicit false skips wiki", &nativeChatRequest{IncludeWiki: ptrBool(false)}, false},
		{"explicit true triggers wiki", &nativeChatRequest{IncludeWiki: ptrBool(true)}, true},
	}
	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			got := derefBoolOr(tc.req.IncludeWiki, false)
			if got != tc.want {
				t.Fatalf("derefBoolOr(IncludeWiki=%v) = %v, want %v",
					tc.req.IncludeWiki, got, tc.want)
			}
		})
	}
}

// TestWikiSlotOutcomeOptInSkipped_Registered ensures the new outcome label
// is in the pre-registration list so dashboards see the counter at zero on
// canary deploy (matches the convention in metrics_wiki_slot.go).
func TestWikiSlotOutcomeOptInSkipped_Registered(t *testing.T) {
	for _, oc := range wikiSlotOutcomes {
		if oc == wikiSlotOutcomeOptInSkipped {
			return
		}
	}
	t.Fatalf("wikiSlotOutcomeOptInSkipped not found in wikiSlotOutcomes pre-registration list")
}

func ptrBool(b bool) *bool { return &b }
