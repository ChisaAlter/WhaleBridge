package usage

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

type ledgerObserver struct {
	starts, stops, rows atomic.Int64
	last                chan Record
	panicRow            bool
}

func (o *ledgerObserver) Serve() func() { o.starts.Add(1); return func() { o.stops.Add(1) } }
func (o *ledgerObserver) Ledger(r Record) {
	o.rows.Add(1)
	if o.panicRow {
		panic("fixture")
	}
	if r.OwnCost != nil {
		*r.OwnCost = 999
	}
	if o.last != nil {
		o.last <- r
	}
}

func TestLedgerObserverLifecycleNoDuplicateAccounting(t *testing.T) {
	sessionHome(t)
	o := &ledgerObserver{last: make(chan Record, 2)}
	remove := Observe(o)
	defer remove()
	stop1 := ServeObservers()
	stop2 := ServeObservers()
	defer stop1()
	defer stop2()
	if o.starts.Load() != 1 {
		t.Fatal("nested serves duplicated worker")
	}
	panicObserver := &ledgerObserver{panicRow: true}
	unpanic := Observe(panicObserver)
	defer unpanic()
	cost := 0.25
	Append(Record{Time: time.Now(), Provider: "fixture", Model: "m", Input: 3, Status: 200, OwnCost: &cost, BodyIn: "body in", BodyOut: "body out", OTel: &OTelSpan{TraceID: "secret"}, SkipOTel: true})
	got := <-o.last
	if got.BodyIn != "" || got.BodyOut != "" || got.OTel != nil || cost != 0.25 {
		t.Fatal("ledger observer shared telemetry or cost ownership")
	}
	if rows := Load(time.Time{}); len(rows) != 1 || rows[0].Input != 3 || rows[0].OwnCost == nil || *rows[0].OwnCost != 0.25 {
		t.Fatalf("observer altered or duplicated ledger: %+v", rows)
	}
	if o.rows.Load() != 1 || panicObserver.rows.Load() != 1 {
		t.Fatal("append did not notify exactly once")
	}
	stop1()
	if o.stops.Load() != 0 {
		t.Fatal("first lease stopped shared worker")
	}
	remove()
	remove()
	if o.stops.Load() != 1 {
		t.Fatal("unregister did not stop once")
	}
	Append(Record{Provider: "fixture", Model: "after", Status: 200})
	if o.rows.Load() != 1 {
		t.Fatal("unregistered observer notified")
	}
	stop2()
	stop2()
	if panicObserver.stops.Load() != 1 {
		t.Fatal("remaining observer not stopped once")
	}
}

func TestLedgerObserverDoesNotDuplicateOTel(t *testing.T) {
	sessionHome(t)
	for name, value := range map[string]string{"MAGPIE_OTEL_ENABLED": "true", "MAGPIE_OTEL_ENDPOINT": "http://127.0.0.1:1", "MAGPIE_OTEL_BODIES": "false", "MAGPIE_OTEL_METRICS": "false", "MAGPIE_OTEL_HEADERS": ""} {
		t.Setenv(name, value)
	}
	// No exporter worker or network starts. Inspect exactly what Append offers
	// to the existing OTel queue versus ExportOTel's ledger-independent path.
	e := newOTelExporter()
	previous := otel.Swap(e)
	defer func() { otel.Store(previous); e.cancel() }()
	o := &ledgerObserver{}
	remove := Observe(o)
	defer remove()
	stop := ServeObservers()
	defer stop()
	Append(Record{Provider: "fixture", Model: "one", Status: 200})
	if len(e.queue) != 1 || o.rows.Load() != 1 {
		t.Fatal("Append duplicate export or missing ledger notification")
	}
	ExportOTel(Record{Provider: "fixture", Model: "trace-only", Status: 200})
	if len(e.queue) != 2 || o.rows.Load() != 1 {
		t.Fatal("trace-only export was counted as a ledger row")
	}
	Append(Record{Provider: "fixture", Model: "skip-export", Status: 200, SkipOTel: true})
	if len(e.queue) != 2 || o.rows.Load() != 2 || len(Load(time.Time{})) != 2 {
		t.Fatal("SkipOTel contract changed")
	}
}

func TestLedgerObserverOnlySuccessfulWritesNotify(t *testing.T) {
	sessionHome(t)
	o := &ledgerObserver{}
	remove := Observe(o)
	defer remove()
	stop := ServeObservers()
	defer stop()
	if err := os.MkdirAll(Path(), 0700); err != nil {
		t.Fatal(err)
	}
	Append(Record{Provider: "fixture", Model: "failed-write", Status: 200})
	if o.rows.Load() != 0 {
		t.Fatal("observer saw row not written to ledger")
	}
	if filepath.Dir(Path()) != settings.Dir() {
		t.Fatal("fixture escaped component directory")
	}
}
