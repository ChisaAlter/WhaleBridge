package provider

import (
	"reflect"
	"slices"
	"testing"
)

func TestWhaleBridgeSearchOrderOffAndRestore(t *testing.T) {
	isolate(t)
	for _, vendor := range []string{"tavily", "brave", "deepseek"} {
		if err := SetSearchAPI(SearchAPI{Vendor: vendor, Key: "fixture-key-" + vendor, URL: "http://127.0.0.1:1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetSearchAPI(SearchAPI{Vendor: "tavily", Pick: SearchPickOrder}); err != nil {
		t.Fatal(err)
	}
	if err := SwitchSearchAPI("tavily", false); err != nil {
		t.Fatal(err)
	}
	if err := SetSearchAPI(SearchAPI{Vendor: "tavily", URL: "http://127.0.0.1:2"}); err != nil {
		t.Fatal(err)
	}
	as := StoredSearchAPIs()
	if !as[0].Off || as[0].Pick != SearchPickOrder || as[0].Key != "fixture-key-tavily" {
		t.Fatalf("edit lost hidden fields %+v", as[0])
	}
	if got := SearchAPIs(); len(got) != 2 || got[0].Vendor != "brave" || got[1].Vendor != "deepseek" {
		t.Fatalf("off API ready: %+v", got)
	}
	if err := ArrangeSearchAPIs([]string{"deepseek", "tavily"}); err != nil {
		t.Fatal(err)
	}
	before := StoredSearchAPIs()
	for _, bad := range [][]string{{"brave", "brave"}, {"missing"}} {
		if err := ArrangeSearchAPIs(bad); err == nil {
			t.Fatal("bad order accepted")
		}
		if !reflect.DeepEqual(before, StoredSearchAPIs()) {
			t.Fatal("bad order partially saved")
		}
	}
	if err := RestoreSearchAPIs([]SearchAPI{{Vendor: "brave", Off: true}, {Vendor: "tavily", Pick: SearchPickOrder}, {Vendor: "brave", Key: "duplicate"}}); err != nil {
		t.Fatal(err)
	}
	as = StoredSearchAPIs()
	if len(as) != 3 || as[0].Vendor != "brave" || !as[0].Off || as[0].Key != "fixture-key-brave" || as[1].Vendor != "tavily" || as[1].Off || as[2].Vendor != "deepseek" {
		t.Fatalf("restore order/state %+v", as)
	}
	if err := SetSearchAPI(SearchAPI{Vendor: "tavily", Pick: "turns"}); err != nil {
		t.Fatal(err)
	}
	if StoredSearchAPIs()[1].Pick != SearchPickTurns {
		t.Fatal("explicit turns did not reset strategy")
	}
	if err := SetSearchAPI(SearchAPI{Vendor: "tavily", Pick: "unknown"}); err == nil {
		t.Fatal("invalid pick accepted")
	}
	if err := MirrorSearchAPIs([]SearchAPI{{Vendor: "deepseek", Off: true}, {Vendor: "tavily", Pick: SearchPickOrder}}); err != nil {
		t.Fatal(err)
	}
	as = StoredSearchAPIs()
	if len(as) != 2 || as[0].Vendor != "deepseek" || !as[0].Off || as[0].Key != "fixture-key-deepseek" || as[1].Pick != SearchPickOrder {
		t.Fatalf("mirror %+v", as)
	}
	if v, ok := SearchVendorOf("deepseek"); !ok || v.KeyOptional || v.Base != "https://api.deepseek.com" {
		t.Fatalf("DeepSeek vendor %+v", v)
	}
	keys := SearchAPI{Key: "tvly-abcdefghijklmnop, second-key; second-key\nthird-key"}.Keys()
	if !slices.Equal(keys, []string{"tvly-abcdefghijklmnop", "second-key", "third-key"}) {
		t.Fatalf("keys %v", keys)
	}
	if got := MaskSearchKey(keys[0]); got != "tvly-…mnop" {
		t.Fatalf("masked key %q", got)
	}
}
