package provider

import (
	"testing"
	"time"
)

func TestQuotaRestSavedAndBounded(t *testing.T) {
	signIn(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "http://127.0.0.1:9/v1", Key: "fake-key", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"relay", "codex"} {
		if err := SetQuotaRest(id, 45); err != nil {
			t.Fatal(err)
		}
		for _, bad := range []int{-1, MaxQuotaRest + 1} {
			if err := SetQuotaRest(id, bad); err == nil {
				t.Fatalf("%s accepted invalid minutes %d", id, bad)
			}
		}
		p, err := Find(id)
		if err != nil || p.QuotaRestFor() != 45*time.Minute {
			t.Fatalf("%s lost its saved rest after refusal: %+v %v", id, p, err)
		}
		p.QuotaRest = MaxQuotaRest + 1
		if err := Save(*p); err != nil {
			t.Fatal(err)
		}
		p, err = Find(id)
		if err != nil || p.QuotaRestFor() != time.Duration(MaxQuotaRest)*time.Minute {
			t.Fatalf("%s save past the bound: %+v %v", id, p, err)
		}
		if err := SetQuotaRest(id, 0); err != nil {
			t.Fatal(err)
		}
		p, err = Find(id)
		if err != nil || p.QuotaRestFor() != 0 {
			t.Fatalf("%s rest disabled: %+v %v", id, p, err)
		}
	}
}
