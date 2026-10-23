package netproxy

import (
	"net/http"
	"testing"
	"time"
)

func TestWhaleBridgeInstallChecksHTTP2Connections(t *testing.T) {
	base, client := http.DefaultTransport, http.DefaultClient.Transport
	defer func() { http.DefaultTransport, http.DefaultClient.Transport = base, client }()
	tx := &http.Transport{}
	http.DefaultTransport = tx
	Install()
	if tx.HTTP2 == nil || tx.HTTP2.SendPingTimeout != 20*time.Second || tx.HTTP2.PingTimeout != 10*time.Second {
		t.Fatalf("HTTP/2 pool has no silence detection: %+v", tx.HTTP2)
	}
	custom := &http.HTTP2Config{SendPingTimeout: time.Minute, PingTimeout: time.Second}
	tx.HTTP2 = custom
	Install()
	if tx.HTTP2 != custom {
		t.Fatal("an existing HTTP/2 configuration was overwritten")
	}
}
