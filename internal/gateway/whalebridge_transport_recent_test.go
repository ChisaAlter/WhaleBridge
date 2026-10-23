package gateway

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"
	"weak"

	"github.com/ChisaAlter/WhaleBridge/internal/netproxy"
)

// A live HTTP/2 peer answers pings during long silent application work. It
// still completes; Codex's HTTP/1 exception stays separate from that pool.
func TestWhaleBridgeHTTP2HealthKeepsLiveStreamsAndHTTP1Override(t *testing.T) {
	oldTLS, oldHTTP1, oldAfter, oldWait := upstreamTLS, netproxy.HTTP1, netproxy.PingAfter, netproxy.PingWait
	t.Cleanup(func() {
		upstreamTLS, netproxy.HTTP1, netproxy.PingAfter, netproxy.PingWait = oldTLS, oldHTTP1, oldAfter, oldWait
	})
	netproxy.PingAfter, netproxy.PingWait = 20*time.Millisecond, 100*time.Millisecond
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Write([]byte(strconv.Itoa(r.ProtoMajor)))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	upstreamTLS = &tls.Config{InsecureSkipVerify: true} // trusts only this loopback fixture
	netproxy.HTTP1 = nil
	client := &http.Client{Transport: upstreamTransport(), Timeout: 2 * time.Second}
	res, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.ProtoMajor != 2 || string(body) != "2" {
		t.Fatalf("live stream lost or no H2: %s %s", res.Proto, body)
	}
	netproxy.HTTP1 = func(*url.URL) bool { return true }
	client = &http.Client{Transport: upstreamTransport(), Timeout: 2 * time.Second}
	res, err = client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.ProtoMajor != 1 {
		t.Fatal("HTTP/1 exception was lost")
	}
}

func TestWhaleBridgeRecentRingReleasesDroppedBodies(t *testing.T) {
	s := &Server{}
	const most, calls = 40, 120
	bodies := make([]weak.Pointer[byte], calls)
	for i := range calls {
		body := strings.Repeat(strconv.Itoa(i)+"x", 4096)
		bodies[i] = weak.Make(unsafe.StringData(body))
		s.record(Call{Model: "m" + strconv.Itoa(i), RequestBody: body})
	}
	runtime.GC()
	runtime.GC()
	for i := range calls - most {
		if bodies[i].Value() != nil {
			t.Fatalf("dropped call %d still retains a body", i)
		}
	}
	for i := calls - most; i < calls; i++ {
		if bodies[i].Value() == nil {
			t.Fatalf("current call %d lost its body", i)
		}
	}
	got := s.Recent()
	if len(got) != most {
		t.Fatalf("recent count %d", len(got))
	}
	for i, c := range got {
		if c.ID != uint64(calls-i) || c.Model != "m"+strconv.Itoa(calls-1-i) {
			t.Fatalf("recent order or ids changed: %+v", c)
		}
	}
	runtime.KeepAlive(s)
}
