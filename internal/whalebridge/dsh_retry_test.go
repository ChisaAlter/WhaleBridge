package whalebridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A DSH writer holding its config lock at start-up leaves the gateway serving:
// the failure is the sync status, and the channel lands once the lock is free.
func TestDSHSyncRetriesAfterAHeldLock(t *testing.T) {
	newProviderHTTP(t)
	home := os.Getenv("WHALEBRIDGE_DSH_HOME")
	lock := filepath.Join(home, "cordis.patch.yml.lock")
	// This process is alive, so its record is a live writer's.
	if err := os.WriteFile(lock, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	err := syncDSH()
	if err == nil || !strings.Contains(err.Error(), "DSH 配置正在编辑") {
		t.Fatalf("held lock was not reported: %v", err)
	}
	setDSHSyncError(err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { retryDSHSync(ctx, 10*time.Millisecond, 40*time.Millisecond); close(done) }()
	time.Sleep(60 * time.Millisecond)
	if dshSyncError() == "" {
		t.Fatal("the error cleared while the lock was still held")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("retry did not finish after the lock was released")
	}
	if got := dshSyncError(); got != "" {
		t.Fatalf("sync error kept after a successful write: %s", got)
	}
	data, err := os.ReadFile(profilePath(home))
	if err != nil || !strings.Contains(string(data), "whalebridge") {
		t.Fatalf("channel not written: %v %s", err, data)
	}
}
