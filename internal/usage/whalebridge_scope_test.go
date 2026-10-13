package usage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/sessions"
)

func TestComponentUsageDoesNotDiscoverNativeCalls(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Now())
	file := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", "-fixture", "native-session.jsonl")
	line := fmt.Sprintf(`{"parentUuid":"p","isSidechain":false,"type":"assistant","uuid":"u-native","timestamp":"%s","message":{"id":"native-call","role":"assistant","model":"claude-opus-4-7","content":[{"type":"text","text":"fixture"}],"usage":{"input_tokens":999,"output_tokens":111}},"requestId":"native-request","sessionId":"native-session","cwd":"/fixture","entrypoint":"cli"}`, now.Add(-time.Second).Format(time.RFC3339Nano))
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(line+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Establish that the isolated native session is a valid scanner input;
	// otherwise a zero ledger would not prove the component's exclusion.
	if calls := sessions.Calls(time.Time{}); len(calls) != 1 || calls[0].Input != 999 {
		t.Fatalf("native fixture was not readable: %+v", calls)
	}
	old := LogCalls
	LogCalls = nil
	t.Cleanup(func() { LogCalls = old })
	Append(Record{Time: now.Add(-time.Second), Agent: "dsh", Provider: "openai", Model: "m", Input: 7, Output: 3, Status: 200})
	if ledger := LedgerOf(All, Filter{}); len(ledger.Rows) != 1 || ledger.Sum.Input != 7 {
		t.Fatalf("native calls entered component ledger: %+v", ledger)
	}
	if page := QueryPage(All, Filter{}, 0, 10); page.Total != 1 || page.Sum.Input != 7 {
		t.Fatalf("native calls entered default query index: %+v", page)
	}
	if direct := Direct(All); direct.Calls != 0 {
		t.Fatalf("default reader scanned native sessions: %+v", direct)
	}
	if days := OwnDays(time.Time{}); len(days) != 1 || days[now.Format(time.DateOnly)][0].Input != 7 {
		t.Fatalf("native calls entered exported component usage: %+v", days)
	}
}
