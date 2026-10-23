package gwmw

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type testMasker string

func (m testMasker) JSON(b []byte) ([]byte, int)                                { return b, 0 }
func (m testMasker) Text(s string) (string, int)                                { return s, 0 }
func (m testMasker) Restore(s string, _ bool) string                            { return string(m) + s }
func (m testMasker) Writer(w http.ResponseWriter) (http.ResponseWriter, func()) { return w, func() {} }

func TestMaskStackAndCapturedGeneration(t *testing.T) {
	first := Mask(testMasker("first"))
	defer first()
	captured, err := Masking(true)
	if err != nil {
		t.Fatal(err)
	}
	next := Mask(testMasker("next"))
	first()
	first() // removing an earlier generation must leave the newest
	now, err := Masking(true)
	if err != nil || now.Restore("", false) != "next" {
		t.Fatalf("current %v %v", now, err)
	}
	next()
	if _, err := Masking(true); err == nil {
		t.Fatal("required missing masker failed open")
	}
	if m, err := Masking(false); m != nil || err != nil {
		t.Fatal("disabled masking unexpectedly required a service")
	}
	if captured.Restore("", false) != "first" {
		t.Fatal("unregister invalidated an in-flight capture")
	}
}
func TestRefreshGenerationImmediateAndReentrant(t *testing.T) {
	oldCalls, newCalls := 0, 0
	stopOld := Refresh(func() { oldCalls++ })
	defer stopOld()
	var remove func()
	stopNew := Refresh(func() { newCalls++; _, _ = Served(); remove = Mask(testMasker("new")) })
	defer stopNew()
	stopOld() // a stale stop cannot clear the newer producer
	if m, err := Masking(true); err != nil || m.Restore("", false) != "new" {
		t.Fatal("new producer was not refreshed immediately")
	}
	remove()
	if _, err := Masking(true); err != nil {
		t.Fatal(err)
	} // no one-second stale window
	remove()
	if oldCalls != 0 || newCalls != 2 {
		t.Fatalf("refresh calls old=%d new=%d", oldCalls, newCalls)
	}
	stopNew()
	if _, err := Masking(true); err == nil {
		t.Fatal("stopped producer was resurrected")
	}
}
func TestMaskerPanicIsOpaque(t *testing.T) {
	_, err := Safely(func() string { panic("private-original-value") })
	if err == nil || strings.Contains(err.Error(), "private-original-value") {
		t.Fatalf("panic was not safely hidden: %v", err)
	}
}

type testAgent struct {
	name   string
	events *[]string
}

func (a testAgent) Begin(Info) Run { return &testRun{a.name, a.events} }

type testRun struct {
	name   string
	events *[]string
}

func (r *testRun) note(s string) { *r.events = append(*r.events, r.name+":"+s) }
func (r *testRun) Request(body []byte) ([]byte, *Reject) {
	r.note("request")
	return append(body, r.name...), nil
}
func (r *testRun) Tried(body []byte, to To) []byte {
	r.note("try-" + to.ID)
	return append(body, r.name...)
}
func (r *testRun) Wrap(w http.ResponseWriter) (http.ResponseWriter, func()) {
	return w, func() { r.note("done") }
}
func (r *testRun) End() { r.note("end") }
func TestChainKeepsRunsAfterUnregister(t *testing.T) {
	var events []string
	a := Use(testAgent{"a", &events})
	b := Use(testAgent{"b", &events})
	chain := Begin(Info{Protocol: "chat"})
	a()
	b()
	if Begin(Info{}) != nil {
		t.Fatal("removed agents still serve new requests")
	}
	body, reject := chain.Request([]byte("body:"))
	if reject != nil || string(body) != "body:ab" {
		t.Fatal("request chain order")
	}
	if got := string(chain.Tried([]byte("try:"), To{ID: "target"})); got != "try:ab" {
		t.Fatal("per-provider chain order")
	}
	_, done := chain.Wrap(httptest.NewRecorder())
	done()
	chain.End()
	want := []string{"a:request", "b:request", "a:try-target", "b:try-target", "b:done", "a:done", "b:end", "a:end"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("chain events %v; want %v", events, want)
	}
}
