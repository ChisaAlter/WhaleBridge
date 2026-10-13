package provider

import "testing"

func TestCopilotEntitlementLabels(t *testing.T) {
	for _, x := range []struct{ plan, sku, want string }{
		{"individual", "free_educational_quota", "Education"},
		{"free", "free_educational_quota", "Education"},
		{"individual", "unknown-sku", "Pro"},
		{"individual", "free_limited_copilot", "Free"},
		{"individual_pro", "", "Pro+"}, {"business", "", "Business"},
		// the real Max account's (copilot_usage_test.go), not "Individual_max"
		{"individual_max", "free_github_star_quota", "Max"},
		{"individual_edu", "", "Education"},
		{"enterprise", "", "Enterprise"}, {"free", "", "Free"},
		{"", "free_limited_copilot", "Free"}, {"", "unknown-sku", ""},
		{"future_plan", "future-sku", "Future_plan"}, {"", "", ""},
	} {
		e := copilotEntitlement{Plan: x.plan, AccessSKU: x.sku}
		if got := e.label(); got != x.want {
			t.Errorf("%+v: %q, want %q", e, got, x.want)
		}
	}
}
