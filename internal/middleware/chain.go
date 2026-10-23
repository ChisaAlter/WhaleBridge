package middleware

import "github.com/ChisaAlter/WhaleBridge/internal/gwmw"

// Chain adapts the existing per-request JS hooks to the gateway registry.
// The Run pins its generation until End even when registrations are removed.
type Chain struct{}

func (Chain) Begin(info gwmw.Info) gwmw.Run {
	r := Begin(Info{Protocol: info.Protocol, Model: info.Model, Stream: info.Stream, Path: info.Path, Agent: info.Agent})
	if r == nil {
		return nil
	}
	return &chainRun{Run: r}
}

type chainRun struct{ *Run }

func (r *chainRun) Request(body []byte) ([]byte, *gwmw.Reject) {
	b, reject := r.Run.Request(body)
	if reject == nil {
		return b, nil
	}
	return b, &gwmw.Reject{Status: reject.Status, Message: reject.Message}
}
