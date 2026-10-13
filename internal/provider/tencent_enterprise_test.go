package provider

import (
	"strings"
	"testing"
)

const (
	tcPlanKeys = "https://console.cloud.tencent.com/tokenhub/tokenplan"
	tcCNHost   = "https://tokenhub.tencentmaas.com"
	tcIntlHost = "https://tokenhub-intl.tencentmaas.com"
)

func TestTencentCloudEnterprisePlan(t *testing.T) {
	pr := Preset("tencent-cloud")
	for _, c := range []struct{ region, host, keys string }{
		{"enterprise", tcCNHost, tcPlanKeys},
		{"enterprise-intl", tcIntlHost, "https://console.tencentcloud.com/tokenhub/tokenplan"},
	} {
		var r *Region
		for i := range pr.Regions {
			if pr.Regions[i].ID == c.region {
				r = &pr.Regions[i]
			}
		}
		if r == nil || r.Chat != c.host+"/plan/v3" || r.Responses != c.host+"/plan/v3" || r.Anthropic != c.host+"/plan/anthropic" ||
			r.Catalog != "" || r.KeysURL != c.keys || r.Website != "https://cloud.tencent.com/document/product/1823/130659" ||
			len(r.Models) == 0 || r.Models[0] != "auto" {
			t.Fatalf("%s: %+v", c.region, r)
		}
		for _, id := range r.Models {
			if id != strings.ToLower(id) {
				t.Fatalf("%s: model ids are lowercase: %q", c.region, id)
			}
		}
		for _, e := range []endpoints{{chat: r.Chat}, {responses: r.Responses}, {anthropic: r.Anthropic}} {
			im, _ := imported("Tencent", "sk-x", e, nil)
			if im.Preset != "tencent-cloud" || im.KeysURL != c.keys || im.Catalog != "" {
				t.Fatalf("imported from %+v: %+v", e, im)
			}
			if got := im.regionOf(pr); got == nil || got.ID != c.region {
				t.Fatalf("imported from %+v at %+v", e, got)
			}
		}
		p, err := ParseImport("magpie://import?preset=tencent-cloud&region=" + c.region)
		if err != nil || p.Chat != r.Chat || p.Responses != r.Responses || p.Anthropic != r.Anthropic || p.KeysURL != c.keys {
			t.Fatalf("link: %v %+v", err, p)
		}
		if got := p.planModels(nil); len(got) != len(r.Models) || got[0].ID != "auto" {
			t.Fatalf("%s's models: %+v", c.region, got)
		}
		// pay as you go at the same host moved here takes the plan's key
		// page and docs, and no catalog
		q, _ := FromPreset("tencent-cloud")
		q.Chat, q.Responses, q.Anthropic, q.Catalog, q.KeysURL, q.Website = c.host+"/v1", c.host+"/v1", c.host, "tencent-tokenhub", "", ""
		if got := q.regionOf(pr); got == nil || got.ID == c.region {
			t.Fatalf("pay as you go at %+v", got)
		}
		q.Chat, q.Responses, q.Anthropic, q.Catalog = r.Chat, r.Responses, r.Anthropic, ""
		if n := normalize(q); n.KeysURL != c.keys || n.Website != r.Website || n.Catalog != "" {
			t.Fatalf("moved to %s: %+v", c.region, n)
		}
	}
}
