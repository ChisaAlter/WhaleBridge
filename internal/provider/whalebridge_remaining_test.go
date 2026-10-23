package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWhaleBridgeRemainingBalanceMethod(t *testing.T) {
	isolate(t)
	type observed struct{ method, auth, tenant, body string }
	var got []observed
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, observed{r.Method, r.Header.Get("Authorization"), r.Header.Get("X-Tenant"), string(b)})
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"usd_balance":"0.94306040"}`)
	}))
	t.Cleanup(srv.Close)
	p := Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "fixture-key", BalanceURL: srv.URL + "/balance", BalancePath: "$usd_balance", Headers: map[string]string{"X-Tenant": "tenant-{key}"}}
	for _, method := range []string{"", "get", "POST", " post "} {
		p.BalanceMethod = method
		amount, ok, err := Balance(context.Background(), p)
		if err != nil || !ok || amount != "$0.94" {
			t.Fatalf("%q: %q %v %v", method, amount, ok, err)
		}
		want := strings.ToUpper(strings.TrimSpace(method))
		if want == "" {
			want = http.MethodGet
		}
		last := got[len(got)-1]
		if last.method != want || last.auth != "Bearer fixture-key" || last.tenant != "tenant-fixture-key" || last.body != "" {
			t.Fatalf("custom query lost method/signature: %+v", last)
		}
	}
	before := len(got)
	for _, method := range []string{"PUT", "DELETE", "POST\r\nX: value"} {
		p.BalanceMethod = method
		if _, _, err := Balance(t.Context(), p); err == nil || len(got) != before {
			t.Fatal("invalid custom method reached its endpoint")
		}
	}
	p.BalanceMethod = "POST"
	p.BalanceToken = "fixture-balance-token"
	if _, _, err := Balance(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if last := got[len(got)-1]; last.auth != "fixture-balance-token" || last.body != "" {
		t.Fatal("custom POST changed dedicated balance authentication")
	}
}

func TestWhaleBridgeRemainingNousAndSiliconFlow(t *testing.T) {
	isolate(t)
	nous, err := FromPreset("nous")
	if err != nil {
		t.Fatal(err)
	}
	if nous.Chat != "https://inference-api.nousresearch.com/v1" || nous.Anthropic != "" || nous.Responses != "" || nous.Catalog != "" || !Preset("nous").Hosts {
		t.Fatalf("Nous capabilities: %+v", nous)
	}
	nous.Key = "fixture-portal-key"
	if head := AuthHeaders(nous, Chat); head["Authorization"] != "Bearer fixture-portal-key" {
		t.Fatalf("Nous signing: %v", head)
	}
	pr := Preset("siliconflow")
	if pr == nil || len(pr.Regions) != 2 {
		t.Fatal("SiliconFlow's separate sites absent")
	}
	p, err := FromPreset("siliconflow")
	if err != nil {
		t.Fatal(err)
	}
	p.Key = "fixture-cn-only-key"
	p.atRegionOf(pr, pr.Regions[1])
	if p.Chat != "https://api.siliconflow.com/v1" || p.Website != "https://cloud.siliconflow.com" || p.KeysURL != "https://cloud.siliconflow.com/account/ak" {
		t.Fatalf("international site: %+v", p)
	}
	if p.Key != "fixture-cn-only-key" {
		t.Fatal("switching region mutated credentials")
	}
	if src, ok := balanceSourceOf(p); !ok || src.url != "https://api.siliconflow.com/v1/user/info" {
		t.Fatal("international balance selected another site")
	}
	p.atRegionOf(pr, pr.Regions[0])
	if p.Chat != "https://api.siliconflow.cn/v1" {
		t.Fatal("could not return to original site")
	}
}
