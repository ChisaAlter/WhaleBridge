package gateway

import (
	"testing"
	"time"
)

// abort removes a run immediately; its process and output reaper can still be
// finishing. Shutdown must wait for that owner, not just entries in runs.
func TestWhaleBridgeCloseSessionsJoinsRemovedReaper(t *testing.T) {
	b := newSubscriptionBridge()
	r := &subscriptionRun{bridge: b, token: "removed-before-reap", pending: map[string]chan mcpToolResult{}}
	b.runs[r.token] = r
	b.reapers.Add(1)
	r.abort()
	b.mu.Lock()
	remaining := len(b.runs)
	b.mu.Unlock()
	if remaining != 0 {
		t.Fatal("abort did not remove run before reaping")
	}
	release := make(chan struct{})
	reaped := make(chan struct{})
	go func() { <-release; b.reapers.Done(); close(reaped) }()
	closed := make(chan struct{})
	go func() { (&Server{subscription: b}).CloseWhaleBridgeSessions(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("session shutdown ignored a removed run's reaper")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("session shutdown did not join reaper")
	}
	<-reaped
}
