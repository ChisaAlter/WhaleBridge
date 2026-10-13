package gateway

import (
	"crypto/tls"
	"net/http"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/netproxy"
)

// upstreamTLS is what the gateway's transports dial TLS with: nil, the
// system's roots, but for a test trusting its own server.
var upstreamTLS *tls.Config

// upstreamTransport is the gateway's transport to the vendors: a provider
// with a proxy of its own is sent through a transport kept for that proxy
// (#237), and the ChatGPT backend over HTTP/1.1 (netproxy.HTTP1). Every
// other vendor is spoken HTTP/2 to wherever it offers it.
func upstreamTransport() http.RoundTripper {
	return netproxy.Dispatch(&http.Transport{
		Proxy:                 netproxy.Func,
		ResponseHeaderTimeout: 10 * time.Minute,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       upstreamTLS,
	})
}
