package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/gwmw"
	"github.com/ChisaAlter/WhaleBridge/internal/masking"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
	"github.com/ChisaAlter/WhaleBridge/internal/usage"
)

type mergeWatch struct {
	mode           string
	attempts, ends int
	last           usage.Record
}

func (w *mergeWatch) Attempt(r usage.Record) {
	if w.mode == "attempt" {
		panic("observer fixture")
	}
	w.attempts++
	w.last = r
}
func (w *mergeWatch) End(usage.Record) {
	if w.mode == "end" {
		panic("observer fixture")
	}
	w.ends++
}

type mergeObserver struct {
	watch      *mergeWatch
	beginPanic bool
}

func (o mergeObserver) Serve() func()        { return func() {} }
func (o mergeObserver) Ledger(usage.Record)  {}
func (o mergeObserver) Bodies() usage.Bodies { return usage.NoBodies }
func (o mergeObserver) Begin(q usage.Request) usage.Watch {
	if o.beginPanic {
		q.Header.Set("X-Fixture", "changed")
		panic("observer fixture")
	}
	return o.watch
}

func TestMergedRequestObserversIsolateCallbacksAndBodies(t *testing.T) {
	bad := usage.Observe(mergeObserver{beginPanic: true})
	defer bad()
	good := &mergeWatch{}
	remove := usage.Observe(mergeObserver{watch: good})
	defer remove()
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Fixture", "original")
	_, watched := observeRequest(r, "", []byte(`{}`))
	if watched == nil || len(watched.watches) != 1 || r.Header.Get("X-Fixture") != "original" {
		t.Fatal("observer Begin changed the request or interrupted another observer")
	}
	watched.watches = append([]requestWatch{{watch: &mergeWatch{mode: "attempt"}}, {watch: &mergeWatch{mode: "end"}}}, watched.watches...)
	watched.attempt(Call{Time: time.Now(), RequestBody: "private input"}, Try{Start: time.Now()}, "fixture", "", &holdWriter{}, &captureResponseWriter{})
	watched.end(Call{Time: time.Now()}, 1)
	if good.attempts != 1 || good.ends != 1 || good.last.BodyIn != "" || good.last.BodyOut != "" {
		t.Fatal("callback failure or unwanted body reached another observer")
	}
	filtered := watchRecord(usage.Record{BodyIn: "private", BodyOut: "reply"}, usage.NoBodies)
	if filtered.BodyIn != "" || filtered.BodyOut != "" {
		t.Fatal("NoBodies observer received capture")
	}
}

type filmingMasker struct {
	masking.Masker
	swap              func()
	textCalls, writes int
}

func (m *filmingMasker) Text(text string) (string, int) {
	m.textCalls++
	if m.swap != nil {
		f := m.swap
		m.swap = nil
		f()
	}
	return "masked:" + text, 1
}
func (m *filmingMasker) Writer(w http.ResponseWriter) (http.ResponseWriter, func()) {
	m.writes++
	return w, func() {}
}

func TestMergedVideoMaskerIsCapturedForExtraAndReply(t *testing.T) {
	fresh(t)
	t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", "")
	if err := settings.Save(settings.Settings{RedactPersonal: true}); err != nil {
		t.Fatal(err)
	}
	first, next := &filmingMasker{}, &filmingMasker{}
	remove := gwmw.Mask(first)
	defer remove()
	var removeNext func()
	first.swap = func() { remove(); removeNext = gwmw.Mask(next) }
	defer func() {
		if removeNext != nil {
			removeNext()
		}
	}()
	f := filming{Prompt: "prompt", Extra: map[string]any{"negative_prompt": "extra", "nested": []any{"secret"}}}
	_, done, err := maskFilming(httptest.NewRecorder(), &f)
	defer done()
	if err != nil {
		t.Fatal(err)
	}
	if first.textCalls != 3 || first.writes != 1 || next.textCalls != 0 || next.writes != 0 {
		t.Fatalf("masker changed mid-request: first=%+v next=%+v", first, next)
	}
}
