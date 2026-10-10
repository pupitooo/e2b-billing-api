package billing

import (
	"strings"
	"testing"
)

// TestSubscriptionIdentity preserves readable tuple components while escaping
// separators and literal percent sequences, including maximum valid byte lengths.
func TestSubscriptionIdentity(t *testing.T) {
	cases := []struct {
		name     string
		customer string
		addon    string
		wantID   string
	}{
		{name: "assignment identifiers", customer: "acme", addon: "concurrency_pack", wantID: "subscription/acme/concurrency_pack"},
		{name: "separator in customer", customer: "acme/team", addon: "pack", wantID: "subscription/acme%2Fteam/pack"},
		{name: "separator in addon", customer: "acme", addon: "team/pack", wantID: "subscription/acme/team%2Fpack"},
		{name: "literal escape cannot impersonate a separator", customer: "acme%2Fteam", addon: "pack", wantID: "subscription/acme%252Fteam/pack"},
		{name: "spaces and plus signs remain distinct", customer: "acme team", addon: "concurrency+pack", wantID: "subscription/acme%20team/concurrency+pack"},
		{name: "Unicode bytes", customer: "客户", addon: "測定", wantID: "subscription/%E5%AE%A2%E6%88%B7/%E6%B8%AC%E5%AE%9A"},
		{name: "maximum escaped input lengths", customer: strings.Repeat("/", 256), addon: strings.Repeat("/", 256), wantID: "subscription/" + strings.Repeat("%2F", 256) + "/" + strings.Repeat("%2F", 256)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actual := subscriptionIdentity(tc.customer, tc.addon)

			if actual != tc.wantID {
				t.Errorf("subscriptionIdentity(customer=%q addon=%q)=%q; want %q", tc.customer, tc.addon, actual, tc.wantID)
			}
		})
	}
}
